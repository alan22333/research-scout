package zotero

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"research-scout/internal/domain"
	"research-scout/internal/embedding"
	"research-scout/internal/storage"
)

type Fixture struct {
	Version     string              `yaml:"version" json:"version"`
	Connection  FixtureConnection   `yaml:"connection" json:"connection"`
	Collections []FixtureCollection `yaml:"collections" json:"collections"`
	Documents   []FixtureDocument   `yaml:"documents" json:"documents"`
}

type FixtureConnection struct {
	LibraryID   string `yaml:"library_id" json:"library_id"`
	Provider    string `yaml:"provider" json:"provider"`
	LibraryType string `yaml:"library_type" json:"library_type"`
}

type FixtureCollection struct {
	Key       string `yaml:"key" json:"key"`
	Name      string `yaml:"name" json:"name"`
	ParentKey string `yaml:"parent_key" json:"parent_key"`
}

type FixtureDocument struct {
	Key           string   `yaml:"key" json:"key"`
	Title         string   `yaml:"title" json:"title"`
	Abstract      string   `yaml:"abstract" json:"abstract"`
	Year          int      `yaml:"year" json:"year"`
	Venue         string   `yaml:"venue" json:"venue"`
	DOI           string   `yaml:"doi" json:"doi"`
	ItemType      string   `yaml:"item_type" json:"item_type"`
	Collections   []string `yaml:"collections" json:"collections"`
	Fulltext      string   `yaml:"fulltext" json:"fulltext"`
	Authors       []string `yaml:"authors" json:"authors"`
	ZoteroVersion int64    `yaml:"zotero_version" json:"zotero_version"`
}

type ImportResult struct {
	EpochID       string `json:"epoch_id"`
	NewEpoch      bool   `json:"new_epoch"`
	Documents     int    `json:"documents"`
	VersionsAdded int    `json:"versions_added"`
	Chunks        int    `json:"chunks"`
	MetadataOnly  int    `json:"metadata_only"`
	ContentHash   string `json:"content_hash"`
}

func LoadFixture(path string) (Fixture, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Fixture{}, err
	}
	var f Fixture
	if err := yaml.Unmarshal(b, &f); err != nil {
		return Fixture{}, fmt.Errorf("parse fixture: %w", err)
	}
	if err := f.Validate(); err != nil {
		return Fixture{}, err
	}
	return f, nil
}

func (f Fixture) Validate() error {
	if strings.TrimSpace(f.Version) == "" {
		return fmt.Errorf("fixture version is required")
	}
	if strings.TrimSpace(f.Connection.LibraryID) == "" {
		return fmt.Errorf("fixture connection.library_id is required")
	}
	if len(f.Documents) == 0 {
		return fmt.Errorf("fixture must contain documents")
	}
	keys := map[string]bool{}
	for _, d := range f.Documents {
		if strings.TrimSpace(d.Key) == "" || strings.TrimSpace(d.Title) == "" {
			return fmt.Errorf("document key and title are required")
		}
		if keys[d.Key] {
			return fmt.Errorf("duplicate document key %q", d.Key)
		}
		keys[d.Key] = true
	}
	return nil
}

func (f Fixture) Hash() (string, error) {
	// The fixture is already a stable struct. YAML-loaded slices preserve order,
	// which is acceptable for deterministic local fixtures.
	b, err := json.Marshal(f)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func (f Fixture) contentHash(d FixtureDocument) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		f.Version, d.Key, d.Title, d.Abstract, fmt.Sprint(d.Year), d.Venue, normalizeDOI(d.DOI), d.ItemType, d.Fulltext,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func normalizeDOI(doi string) string {
	value := strings.ToLower(strings.TrimSpace(doi))
	value = strings.TrimPrefix(value, "https://doi.org/")
	value = strings.TrimPrefix(value, "http://doi.org/")
	value = strings.TrimPrefix(value, "doi:")
	return strings.TrimSpace(value)
}

type Importer struct {
	Store            *storage.Store
	EmbeddingVersion string
	EmbeddingDim     int
}

func (i Importer) Import(ctx context.Context, workspaceID, actorID string, f Fixture) (ImportResult, error) {
	if i.Store == nil {
		return ImportResult{}, fmt.Errorf("store is required")
	}
	if err := f.Validate(); err != nil {
		return ImportResult{}, err
	}
	hash, err := f.Hash()
	if err != nil {
		return ImportResult{}, err
	}
	if i.EmbeddingVersion == "" {
		i.EmbeddingVersion = embedding.Stage0Version
	}
	if i.EmbeddingDim <= 0 {
		i.EmbeddingDim = 1024
	}

	tx, err := i.Store.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ImportResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "zotero-import:"+workspaceID); err != nil {
		return ImportResult{}, fmt.Errorf("lock fixture import: %w", err)
	}

	var activeID, activeHash string
	err = tx.QueryRow(ctx, `
		SELECT id, content_hash FROM index_epochs
		WHERE workspace_id=$1 AND status='active' AND retired_at IS NULL
		LIMIT 1
	`, workspaceID).Scan(&activeID, &activeHash)
	if err != nil && err != pgx.ErrNoRows {
		return ImportResult{}, err
	}
	if activeID != "" && activeHash == hash {
		var docs, chunks int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM index_epoch_documents WHERE epoch_id=$1`, activeID).Scan(&docs); err != nil {
			return ImportResult{}, err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM chunks WHERE index_epoch_id=$1`, activeID).Scan(&chunks); err != nil {
			return ImportResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return ImportResult{}, err
		}
		return ImportResult{EpochID: activeID, Documents: docs, Chunks: chunks, MetadataOnly: countMetadataOnly(f.Documents), ContentHash: hash}, nil
	}

	connectionID, err := ensureConnection(ctx, tx, workspaceID, actorID, f.Connection)
	if err != nil {
		return ImportResult{}, err
	}

	collectionIDs := map[string]string{}
	for _, c := range f.Collections {
		id, err := ensureCollection(ctx, tx, workspaceID, connectionID, c)
		if err != nil {
			return ImportResult{}, err
		}
		collectionIDs[c.Key] = id
	}

	documentIDs := map[string]string{}
	versionIDs := map[string]string{}
	versionNumbers := map[string]int{}
	versionsAdded := 0
	metadataOnly := 0
	for _, d := range f.Documents {
		docID, versionID, versionNo, added, status, evidenceType, err := ensureDocument(ctx, tx, workspaceID, connectionID, f.Version, d)
		if err != nil {
			return ImportResult{}, err
		}
		documentIDs[d.Key] = docID
		versionIDs[d.Key] = versionID
		versionNumbers[d.Key] = versionNo
		if added {
			versionsAdded++
		}
		if status == domain.IndexMetadataOnly {
			metadataOnly++
		}
		_ = evidenceType

		desiredCollections := make([]string, 0, len(d.Collections))
		for _, key := range d.Collections {
			id, ok := collectionIDs[key]
			if !ok {
				return ImportResult{}, fmt.Errorf("document %s references unknown collection %s", d.Key, key)
			}
			desiredCollections = append(desiredCollections, id)
		}
		if err := replaceDocumentCollections(ctx, tx, workspaceID, docID, desiredCollections); err != nil {
			return ImportResult{}, err
		}
	}

	epochID := storage.StableID("epoch", workspaceID, hash)
	if _, err := tx.Exec(ctx, `
		INSERT INTO index_epochs
			(id, workspace_id, status, embedding_version, retrieval_config_version, content_hash)
		VALUES ($1,$2,'building',$3,'retrieval-v1',$4)
		ON CONFLICT (id) DO UPDATE SET status='building', published_at=NULL, retired_at=NULL
	`, epochID, workspaceID, i.EmbeddingVersion, hash); err != nil {
		return ImportResult{}, fmt.Errorf("create building epoch: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM chunks WHERE index_epoch_id=$1`, epochID); err != nil {
		return ImportResult{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM index_epoch_documents WHERE epoch_id=$1`, epochID); err != nil {
		return ImportResult{}, err
	}

	chunkCount := 0
	for _, d := range f.Documents {
		if _, err := tx.Exec(ctx, `
			INSERT INTO index_epoch_documents (epoch_id, workspace_id, document_id, document_version_id)
			VALUES ($1,$2,$3,$4)
		`, epochID, workspaceID, documentIDs[d.Key], versionIDs[d.Key]); err != nil {
			return ImportResult{}, fmt.Errorf("insert epoch document %s: %w", d.Key, err)
		}
		if _, err := tx.Exec(ctx, `UPDATE documents SET active_version_id=$2, updated_at=now() WHERE id=$1 AND workspace_id=$3`, documentIDs[d.Key], versionIDs[d.Key], workspaceID); err != nil {
			return ImportResult{}, err
		}
		chunks := buildChunks(epochID, workspaceID, documentIDs[d.Key], versionIDs[d.Key], d, versionNumbers[d.Key], i.EmbeddingDim, i.EmbeddingVersion)
		for _, c := range chunks {
			vectorString := embedding.FormatVector(c.Vector)
			if _, err := tx.Exec(ctx, `
				INSERT INTO chunks
					(id,index_epoch_id,document_version_id,document_id,workspace_id,ordinal,section,
					 char_start,char_end,content,token_count,search_tsv,embedding,embedding_version)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,to_tsvector('english',$10),$12::vector,$13)
				ON CONFLICT (id) DO NOTHING
			`, c.ID, epochID, versionIDs[d.Key], documentIDs[d.Key], workspaceID, c.Ordinal, c.Section,
				c.CharStart, c.CharEnd, c.Content, c.TokenCount, vectorString, i.EmbeddingVersion); err != nil {
				return ImportResult{}, fmt.Errorf("insert chunk %s/%d: %w", d.Key, c.Ordinal, err)
			}
			chunkCount++
		}
	}

	if chunkCount == 0 {
		return ImportResult{}, fmt.Errorf("fixture has no fulltext or abstract content to index")
	}

	var buildingCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM chunks WHERE index_epoch_id=$1`, epochID).Scan(&buildingCount); err != nil {
		return ImportResult{}, err
	}
	if buildingCount != chunkCount {
		return ImportResult{}, fmt.Errorf("epoch validation failed: expected %d chunks, got %d", chunkCount, buildingCount)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE index_epochs
		SET status='retired', retired_at=now()
		WHERE workspace_id=$1 AND status='active' AND id<>$2
	`, workspaceID, epochID); err != nil {
		return ImportResult{}, fmt.Errorf("retire old epoch: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE index_epochs
		SET status='active', published_at=now(), retired_at=NULL
		WHERE id=$1 AND workspace_id=$2 AND status='building'
	`, epochID, workspaceID)
	if err != nil {
		return ImportResult{}, fmt.Errorf("publish epoch: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ImportResult{}, fmt.Errorf("epoch publish did not transition exactly one building epoch")
	}
	if err := tx.Commit(ctx); err != nil {
		return ImportResult{}, err
	}
	return ImportResult{
		EpochID: epochID, NewEpoch: true, Documents: len(f.Documents), VersionsAdded: versionsAdded,
		Chunks: chunkCount, MetadataOnly: metadataOnly, ContentHash: hash,
	}, nil
}

func ensureConnection(ctx context.Context, tx pgx.Tx, workspaceID, actorID string, c FixtureConnection) (string, error) {
	provider := c.Provider
	if provider == "" {
		provider = "web_api"
	}
	libraryType := c.LibraryType
	if libraryType == "" {
		libraryType = "user"
	}
	id := storage.StableID("conn", workspaceID, provider, c.LibraryID)
	if _, err := tx.Exec(ctx, `
		INSERT INTO zotero_connections
			(id,workspace_id,actor_id,provider,library_type,library_id,status,coverage_jsonb)
		VALUES ($1,$2,$3,$4,$5,$6,'healthy','{"fixture":true,"local_only_warning":true}'::jsonb)
		ON CONFLICT (id) DO UPDATE SET status='healthy', revoked_at=NULL, purge_at=NULL, updated_at=now()
	`, id, workspaceID, actorID, provider, libraryType, c.LibraryID); err != nil {
		return "", fmt.Errorf("ensure connection: %w", err)
	}
	return id, nil
}

func ensureCollection(ctx context.Context, tx pgx.Tx, workspaceID, connectionID string, c FixtureCollection) (string, error) {
	id := storage.StableID("collection", connectionID, c.Key)
	if _, err := tx.Exec(ctx, `
		INSERT INTO collections (id,workspace_id,connection_id,zotero_key,parent_key,name,is_deleted)
		VALUES ($1,$2,$3,$4,NULLIF($5,''),$6,false)
		ON CONFLICT (id) DO UPDATE SET parent_key=EXCLUDED.parent_key,name=EXCLUDED.name,is_deleted=false,updated_at=now()
	`, id, workspaceID, connectionID, c.Key, c.ParentKey, c.Name); err != nil {
		return "", fmt.Errorf("ensure collection %s: %w", c.Key, err)
	}
	return id, nil
}

func ensureDocument(ctx context.Context, tx pgx.Tx, workspaceID, connectionID, fixtureVersion string, d FixtureDocument) (string, string, int, bool, domain.IndexStatus, *domain.EvidenceType, error) {
	docID := storage.StableID("doc", connectionID, d.Key)
	itemType := d.ItemType
	if itemType == "" {
		itemType = "journalArticle"
	}
	status, evidenceType, sourceKind := documentState(d)
	var evidenceValue any
	if evidenceType != nil {
		evidenceValue = string(*evidenceType)
	}
	authors, err := storage.JSON(d.Authors)
	if err != nil {
		return "", "", 0, false, "", nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO documents
			(id,workspace_id,connection_id,zotero_item_key,item_type,title,abstract,year,venue,doi_normalized,
			 authors_json,index_status,evidence_type,zotero_version,content_hash,removed_at,purge_after)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12,$13,$14,$15,NULL,NULL)
		ON CONFLICT (id) DO UPDATE SET
			item_type=EXCLUDED.item_type,title=EXCLUDED.title,abstract=EXCLUDED.abstract,year=EXCLUDED.year,
			venue=EXCLUDED.venue,doi_normalized=EXCLUDED.doi_normalized,authors_json=EXCLUDED.authors_json,
			index_status=EXCLUDED.index_status,evidence_type=EXCLUDED.evidence_type,zotero_version=EXCLUDED.zotero_version,
			content_hash=EXCLUDED.content_hash,last_seen_at=now(),updated_at=now(),removed_at=NULL,purge_after=NULL
	`, docID, workspaceID, connectionID, d.Key, itemType, d.Title, d.Abstract, nullableInt(d.Year), d.Venue, normalizeDOI(d.DOI),
		authors, status, evidenceValue, d.ZoteroVersion, ""); err != nil {
		return "", "", 0, false, "", nil, fmt.Errorf("upsert document %s: %w", d.Key, err)
	}

	contentHash := Fixture{Version: fixtureVersion}.contentHash(d)
	var latestID string
	var latestNo int
	var latestHash string
	err = tx.QueryRow(ctx, `
		SELECT id, version_no, content_hash
		FROM document_versions
		WHERE document_id=$1
		ORDER BY version_no DESC
		LIMIT 1
	`, docID).Scan(&latestID, &latestNo, &latestHash)
	if err != nil && err != pgx.ErrNoRows {
		return "", "", 0, false, "", nil, err
	}
	added := false
	versionID := latestID
	versionNo := latestNo
	if latestID == "" || latestHash != contentHash {
		versionNo = latestNo + 1
		versionID = storage.StableID("dv", docID, fmt.Sprint(versionNo), contentHash)
		if _, err := tx.Exec(ctx, `
			INSERT INTO document_versions
				(id,document_id,workspace_id,version_no,source_kind,fulltext,abstract,content_hash)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		`, versionID, docID, workspaceID, versionNo, sourceKind, d.Fulltext, d.Abstract, contentHash); err != nil {
			return "", "", 0, false, "", nil, fmt.Errorf("insert document version %s: %w", d.Key, err)
		}
		added = true
	}
	if _, err := tx.Exec(ctx, `UPDATE documents SET active_version_id=$2, content_hash=$3 WHERE id=$1`, docID, versionID, contentHash); err != nil {
		return "", "", 0, false, "", nil, err
	}
	return docID, versionID, versionNo, added, status, evidenceType, nil
}

func documentState(d FixtureDocument) (domain.IndexStatus, *domain.EvidenceType, string) {
	if strings.TrimSpace(d.Fulltext) != "" {
		t := domain.EvidencePersonalFulltext
		return domain.IndexIndexed, &t, "fulltext"
	}
	if strings.TrimSpace(d.Abstract) != "" {
		t := domain.EvidenceStructuredAbstract
		return domain.IndexIndexed, &t, "abstract"
	}
	return domain.IndexMetadataOnly, nil, "metadata"
}

func replaceDocumentCollections(ctx context.Context, tx pgx.Tx, workspaceID, documentID string, collectionIDs []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM document_collections WHERE document_id=$1`, documentID); err != nil {
		return err
	}
	for _, collectionID := range collectionIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO document_collections (document_id, collection_id, workspace_id)
			VALUES ($1,$2,$3)
			ON CONFLICT DO NOTHING
		`, documentID, collectionID, workspaceID); err != nil {
			return err
		}
	}
	return nil
}

func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

type stagedChunk struct {
	ID         string
	Ordinal    int
	Section    string
	CharStart  int
	CharEnd    int
	Content    string
	TokenCount int
	Vector     []float32
}

func buildChunks(epochID, workspaceID, documentID, versionID string, d FixtureDocument, versionNo, dim int, embeddingVersion string) []stagedChunk {
	type source struct {
		section string
		text    string
	}
	var sources []source
	if strings.TrimSpace(d.Abstract) != "" {
		sources = append(sources, source{section: "abstract", text: strings.TrimSpace(d.Abstract)})
	}
	if strings.TrimSpace(d.Fulltext) != "" {
		for idx, text := range splitText(d.Fulltext, 1800) {
			sources = append(sources, source{section: fmt.Sprintf("body-%d", idx+1), text: text})
		}
	}
	out := make([]stagedChunk, 0, len(sources))
	stub := embedding.Stub{Dim: dim}
	cursor := 0
	for idx, src := range sources {
		start := strings.Index(d.Fulltext+d.Abstract, src.text)
		if start < 0 {
			start = cursor
		}
		end := start + len(src.text)
		cursor = end
		chunk := domain.Chunk{IndexEpochID: epochID, DocumentVersionID: versionID, Ordinal: idx}
		out = append(out, stagedChunk{
			ID: chunkKey(chunk), Ordinal: idx, Section: src.section, CharStart: start, CharEnd: end,
			Content: src.text, TokenCount: len(strings.Fields(src.text)), Vector: stub.Vector(src.text),
		})
	}
	return out
}

func chunkKey(c domain.Chunk) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", c.IndexEpochID, c.DocumentVersionID, c.Ordinal)))
	return "chunk-" + hex.EncodeToString(sum[:])[:32]
}

func splitText(text string, maxChars int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	paras := strings.Split(text, "\n\n")
	var out []string
	for _, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		for len(p) > maxChars {
			cut := maxChars
			if i := strings.LastIndexAny(p[:maxChars], ".!? "); i > maxChars/2 {
				cut = i + 1
			}
			out = append(out, strings.TrimSpace(p[:cut]))
			p = strings.TrimSpace(p[cut:])
		}
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func countMetadataOnly(docs []FixtureDocument) int {
	n := 0
	for _, d := range docs {
		if strings.TrimSpace(d.Fulltext) == "" && strings.TrimSpace(d.Abstract) == "" {
			n++
		}
	}
	return n
}

func SortedDocumentKeys(f Fixture) []string {
	keys := make([]string, 0, len(f.Documents))
	for _, d := range f.Documents {
		keys = append(keys, d.Key)
	}
	sort.Strings(keys)
	return keys
}
