package zotero

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"research-scout/internal/domain"
	"research-scout/internal/embedding"
	"research-scout/internal/storage"
)

// SyncMode selects between a full rebuild and a version-based incremental sync.
type SyncMode string

const (
	SyncModeFull        SyncMode = "full"
	SyncModeIncremental SyncMode = "incremental"
)

// SyncResult summarises one sync run. It never contains credential material.
type SyncResult struct {
	EpochID       string `json:"epoch_id"`
	NewEpoch      bool   `json:"new_epoch"`
	Documents     int    `json:"documents"`
	VersionsAdded int    `json:"versions_added"`
	Chunks        int    `json:"chunks"`
	Fulltext      int    `json:"fulltext"`
	Abstracts     int    `json:"abstracts"`
	MetadataOnly  int    `json:"metadata_only"`
	Deleted       int    `json:"deleted"`
}

// SyncConfig carries version pins and an injectable clock.
type SyncConfig struct {
	EmbeddingVersion       string
	EmbeddingDim           int
	RetrievalConfigVersion string
	Now                    func() time.Time
}

// CredentialStore persists opaque encrypted credential bytes keyed by
// workspace and connection. Implementations must never persist plaintext.
type CredentialStore interface {
	Put(ctx context.Context, workspaceID, connectionID string, secret []byte) error
	Get(ctx context.Context, workspaceID, connectionID string) ([]byte, error)
	Delete(ctx context.Context, workspaceID, connectionID string) error
}

// Syncer pulls a Zotero personal library through a read-only Source and
// publishes a new index epoch atomically. A failed run never mutates the
// active epoch.
type Syncer struct {
	Store       *storage.Store
	Source      Source
	Credentials CredentialStore
	Config      SyncConfig
}

const tombstoneRetention = 30 * 24 * time.Hour

func (s *Syncer) now() time.Time {
	if s.Config.Now != nil {
		return s.Config.Now()
	}
	return time.Now().UTC()
}

func (s *Syncer) embeddingVersion() string {
	if strings.TrimSpace(s.Config.EmbeddingVersion) == "" {
		return embedding.Stage0Version
	}
	return s.Config.EmbeddingVersion
}

func (s *Syncer) embeddingDim() int {
	if s.Config.EmbeddingDim <= 0 {
		return 1024
	}
	return s.Config.EmbeddingDim
}

func (s *Syncer) retrievalVersion() string {
	if strings.TrimSpace(s.Config.RetrievalConfigVersion) == "" {
		return "retrieval-v1"
	}
	return s.Config.RetrievalConfigVersion
}

type stagedDocument struct {
	Key           string
	Version       int64
	ItemType      string
	Title         string
	Abstract      string
	Year          int
	Venue         string
	DOI           string
	Authors       []string
	Collections   []string
	ZoteroVersion int64
	Fulltext      string
	State         domain.IndexStatus
	EvidenceType  *domain.EvidenceType
	SourceKind    string
	ContentHash   string
}

// Sync executes one full or incremental sync for the workspace connection.
func (s *Syncer) Sync(ctx context.Context, workspaceID, actorID string, cred Credential, mode SyncMode) (SyncResult, error) {
	if s.Store == nil || s.Source == nil {
		return SyncResult{}, fmt.Errorf("syncer requires store and source")
	}
	if mode != SyncModeFull && mode != SyncModeIncremental {
		return SyncResult{}, fmt.Errorf("unsupported sync mode %q", mode)
	}
	now := s.now()

	// 1. Resolve credentials + validate identity before touching the database.
	info, err := s.Source.Validate(ctx, cred)
	if err != nil {
		return SyncResult{}, err
	}
	libraryID := strings.TrimSpace(cred.LibraryID)
	if libraryID == "" {
		libraryID = info.LibraryID
	}
	if libraryID == "" {
		libraryID = info.UserID
	}
	libraryType := strings.TrimSpace(cred.LibraryType)
	if libraryType == "" {
		libraryType = info.LibraryType
	}
	if libraryType == "" {
		libraryType = "user"
	}
	resolved := Credential{APIKey: cred.APIKey, LibraryType: libraryType, LibraryID: libraryID}

	// 2. Ensure the connection + encrypted credential are persisted.
	connectionID, fromVersion, err := s.ensureConnection(ctx, workspaceID, actorID, resolved)
	if err != nil {
		return SyncResult{}, err
	}
	if s.Credentials != nil {
		if err := s.Credentials.Put(ctx, workspaceID, connectionID, []byte(cred.APIKey)); err != nil {
			return SyncResult{}, fmt.Errorf("store credential: %w", err)
		}
	}

	// 3. Load collections first so documents can be mapped to collection ids.
	collections, err := s.loadCollections(ctx, resolved)
	if err != nil {
		return SyncResult{}, err
	}

	// 4. Pull the change set. Full sync always starts from version 0.
	since := int64(0)
	if mode == SyncModeIncremental {
		since = fromVersion
		head, headErr := s.Source.GetLibraryVersion(ctx, resolved)
		if headErr != nil {
			return SyncResult{}, headErr
		}
		if head == fromVersion {
			return s.noopResult(ctx, workspaceID, mode), nil
		}
	}
	items, deleted, toVersion, err := s.loadChanges(ctx, resolved, since)
	if err != nil {
		return SyncResult{}, err
	}

	// 5. Fetch fulltext for each item's attachments (read-only, outside a tx).
	staged, err := s.stageDocuments(ctx, resolved, items)
	if err != nil {
		return SyncResult{}, err
	}

	// 6. Persist atomically. Any failure keeps the previous active epoch.
	runID := storage.StableID("syncrun", connectionID, string(mode), fmt.Sprint(now.UnixNano()))
	result, err := s.persist(ctx, persistInput{
		workspaceID: workspaceID, connectionID: connectionID,
		runID: runID, mode: mode, fromVersion: fromVersion, toVersion: toVersion,
		now: now, collections: collections, staged: staged, deleted: deleted,
	})
	if err != nil {
		return SyncResult{}, err
	}
	return result, nil
}

type persistInput struct {
	workspaceID  string
	connectionID string
	runID        string
	mode         SyncMode
	fromVersion  int64
	toVersion    int64
	now          time.Time
	collections  []Collection
	staged       []stagedDocument
	deleted      []string
}

func (s *Syncer) persist(ctx context.Context, in persistInput) (SyncResult, error) {
	tx, err := s.Store.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SyncResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "zotero-sync:"+in.workspaceID); err != nil {
		return SyncResult{}, fmt.Errorf("lock workspace sync: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO zotero_sync_runs (id, connection_id, workspace_id, mode, status, from_version, to_version, started_at)
		VALUES ($1,$2,$3,$4,'running',$5,$6,$7)
		ON CONFLICT (id) DO NOTHING
	`, in.runID, in.connectionID, in.workspaceID, string(in.mode), in.fromVersion, in.toVersion, in.now); err != nil {
		return SyncResult{}, fmt.Errorf("start sync run: %w", err)
	}

	collectionIDs, err := s.upsertCollections(ctx, tx, in.workspaceID, in.connectionID, in.collections)
	if err != nil {
		return SyncResult{}, err
	}

	versionIDs := map[string]string{}
	versionNumbers := map[string]int{}
	docIDs := map[string]string{}
	versionsAdded := 0
	var counts struct{ fulltext, abstracts, metadataOnly int }
	activeHashes := make([]string, 0, len(in.staged))

	for _, doc := range in.staged {
		docID, versionID, versionNo, added, err := s.upsertDocument(ctx, tx, in.workspaceID, in.connectionID, doc)
		if err != nil {
			return SyncResult{}, err
		}
		if added {
			versionsAdded++
		}
		docIDs[doc.Key] = docID
		versionIDs[doc.Key] = versionID
		versionNumbers[doc.Key] = versionNo
		activeHashes = append(activeHashes, doc.Key+"\x00"+doc.ContentHash)
		switch doc.State {
		case domain.IndexIndexed:
			if doc.EvidenceType != nil && *doc.EvidenceType == domain.EvidencePersonalFulltext {
				counts.fulltext++
			} else {
				counts.abstracts++
			}
		default:
			counts.metadataOnly++
		}

		desired := make([]string, 0, len(doc.Collections))
		for _, key := range doc.Collections {
			if id, ok := collectionIDs[key]; ok {
				desired = append(desired, id)
			}
		}
		if err := replaceDocumentCollections(ctx, tx, in.workspaceID, docID, desired); err != nil {
			return SyncResult{}, err
		}
	}

	deletedCount := 0
	for _, key := range in.deleted {
		n, err := s.tombstoneDocument(ctx, tx, in.workspaceID, in.connectionID, key, in.now)
		if err != nil {
			return SyncResult{}, err
		}
		deletedCount += n
	}

	// Active document set drives epoch content hash + chunk population.
	activeRows, err := tx.Query(ctx, `
		SELECT d.id, d.zotero_item_key, d.active_version_id, d.content_hash,
		       COALESCE(v.fulltext,''), COALESCE(v.abstract,''), COALESCE(v.source_kind,'metadata')
		FROM documents d
		JOIN document_versions v ON v.id = d.active_version_id
		WHERE d.workspace_id=$1 AND d.connection_id=$2 AND d.removed_at IS NULL
		ORDER BY d.zotero_item_key
	`, in.workspaceID, in.connectionID)
	if err != nil {
		return SyncResult{}, err
	}
	type activeDoc struct {
		id, key, versionID, contentHash, fulltext, abstract, sourceKind string
	}
	var activeDocs []activeDoc
	allHashes := make([]string, 0)
	for activeRows.Next() {
		var a activeDoc
		if err := activeRows.Scan(&a.id, &a.key, &a.versionID, &a.contentHash, &a.fulltext, &a.abstract, &a.sourceKind); err != nil {
			activeRows.Close()
			return SyncResult{}, err
		}
		activeDocs = append(activeDocs, a)
		allHashes = append(allHashes, a.key+"\x00"+a.contentHash)
	}
	activeRows.Close()
	if err := activeRows.Err(); err != nil {
		return SyncResult{}, err
	}
	sort.Strings(allHashes)
	aggregate := hashStrings(allHashes)

	if len(activeDocs) == 0 {
		return SyncResult{}, fmt.Errorf("sync produced no indexable documents")
	}

	// Idempotency: identical active content and edition of the epoch already
	// active means nothing to publish.
	activeID, activeHash := s.activeEpoch(ctx, tx, in.workspaceID)
	if activeID != "" && activeHash == aggregate {
		if err := s.finishRun(ctx, tx, in, counts, versionsAdded, deletedCount, 0, "complete", nil); err != nil {
			return SyncResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return SyncResult{}, err
		}
		return SyncResult{
			EpochID: activeID, NewEpoch: false, Documents: len(activeDocs), VersionsAdded: versionsAdded,
			Chunks: 0, Fulltext: counts.fulltext, Abstracts: counts.abstracts,
			MetadataOnly: counts.metadataOnly, Deleted: deletedCount,
		}, nil
	}

	epochID := storage.StableID("epoch", in.workspaceID, aggregate)
	if _, err := tx.Exec(ctx, `
		INSERT INTO index_epochs (id, workspace_id, status, source_sync_run_id, embedding_version, retrieval_config_version, content_hash)
		VALUES ($1,$2,'building',$3,$4,$5,$6)
		ON CONFLICT (id) DO UPDATE SET status='building', source_sync_run_id=EXCLUDED.source_sync_run_id,
			published_at=NULL, retired_at=NULL
	`, epochID, in.workspaceID, in.runID, s.embeddingVersion(), s.retrievalVersion(), aggregate); err != nil {
		return SyncResult{}, fmt.Errorf("create building epoch: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM chunks WHERE index_epoch_id=$1`, epochID); err != nil {
		return SyncResult{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM index_epoch_documents WHERE epoch_id=$1`, epochID); err != nil {
		return SyncResult{}, err
	}

	chunkCount := 0
	dim := s.embeddingDim()
	embedVersion := s.embeddingVersion()
	for _, a := range activeDocs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO index_epoch_documents (epoch_id, workspace_id, document_id, document_version_id)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (epoch_id, document_id) DO NOTHING
		`, epochID, in.workspaceID, a.id, a.versionID); err != nil {
			return SyncResult{}, fmt.Errorf("insert epoch document %s: %w", a.key, err)
		}
		chunks := buildDocumentChunks(epochID, a.id, a.versionID, a.fulltext, a.abstract, a.sourceKind, dim, embedVersion)
		for _, c := range chunks {
			vectorString := embedding.FormatVector(c.Vector)
			if _, err := tx.Exec(ctx, `
				INSERT INTO chunks
					(id,index_epoch_id,document_version_id,document_id,workspace_id,ordinal,section,
					 char_start,char_end,content,token_count,search_tsv,embedding,embedding_version)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,to_tsvector('english',$10),$12::vector,$13)
				ON CONFLICT (id) DO NOTHING
			`, c.ID, epochID, a.versionID, a.id, in.workspaceID, c.Ordinal, c.Section,
				c.CharStart, c.CharEnd, c.Content, c.TokenCount, vectorString, embedVersion); err != nil {
				return SyncResult{}, fmt.Errorf("insert chunk %s/%d: %w", a.key, c.Ordinal, err)
			}
			chunkCount++
		}
	}

	var buildingCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM chunks WHERE index_epoch_id=$1`, epochID).Scan(&buildingCount); err != nil {
		return SyncResult{}, err
	}
	if buildingCount != chunkCount {
		return SyncResult{}, fmt.Errorf("epoch validation failed: expected %d chunks, got %d", chunkCount, buildingCount)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE index_epochs SET status='retired', retired_at=$2
		WHERE workspace_id=$1 AND status='active' AND id<>$3
	`, in.workspaceID, in.now, epochID); err != nil {
		return SyncResult{}, fmt.Errorf("retire old epoch: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE index_epochs SET status='active', published_at=$2, retired_at=NULL
		WHERE id=$1 AND workspace_id=$3 AND status='building'
	`, epochID, in.now, in.workspaceID)
	if err != nil {
		return SyncResult{}, fmt.Errorf("publish epoch: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return SyncResult{}, fmt.Errorf("epoch publish did not transition exactly one building epoch")
	}

	if err := s.finishRun(ctx, tx, in, counts, versionsAdded, deletedCount, chunkCount, "complete", nil); err != nil {
		return SyncResult{}, err
	}
	// Cursor advances only after the epoch is published in the same transaction.
	if _, err := tx.Exec(ctx, `
		UPDATE zotero_connections
		SET last_library_version=$2, last_sync_at=$3, last_success_at=$3, updated_at=$3,
		    coverage_jsonb=$4::jsonb
		WHERE id=$1 AND workspace_id=$5
	`, in.connectionID, in.toVersion, in.now, coverageJSON(counts, deletedCount), in.workspaceID); err != nil {
		return SyncResult{}, fmt.Errorf("advance connection cursor: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return SyncResult{}, err
	}
	return SyncResult{
		EpochID: epochID, NewEpoch: true, Documents: len(activeDocs), VersionsAdded: versionsAdded,
		Chunks: chunkCount, Fulltext: counts.fulltext, Abstracts: counts.abstracts,
		MetadataOnly: counts.metadataOnly, Deleted: deletedCount,
	}, nil
}

func (s *Syncer) ensureConnection(ctx context.Context, workspaceID, actorID string, cred Credential) (string, int64, error) {
	provider := "web_api"
	connectionID := storage.StableID("conn", workspaceID, provider, cred.LibraryID)
	var fromVersion int64
	err := s.Store.Pool().QueryRow(ctx, `
		SELECT last_library_version FROM zotero_connections WHERE id=$1 AND workspace_id=$2
	`, connectionID, workspaceID).Scan(&fromVersion)
	if err != nil && err != pgx.ErrNoRows {
		return "", 0, err
	}
	if _, err := s.Store.Pool().Exec(ctx, `
		INSERT INTO zotero_connections
			(id,workspace_id,actor_id,provider,library_type,library_id,status,last_library_version)
		VALUES ($1,$2,$3,$4,$5,$6,'healthy',0)
		ON CONFLICT (id) DO UPDATE SET status='healthy', revoked_at=NULL, purge_at=NULL,
			library_type=EXCLUDED.library_type, updated_at=now()
	`, connectionID, workspaceID, actorID, provider, cred.LibraryType, cred.LibraryID); err != nil {
		return "", 0, fmt.Errorf("ensure connection: %w", err)
	}
	return connectionID, fromVersion, nil
}

func (s *Syncer) loadCollections(ctx context.Context, cred Credential) ([]Collection, error) {
	var all []Collection
	cursor := CollectionCursor{}
	for {
		page, err := s.Source.ListCollections(ctx, cred, cursor)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if page.Next == nil {
			break
		}
		cursor = *page.Next
	}
	return all, nil
}

func (s *Syncer) loadChanges(ctx context.Context, cred Credential, since int64) ([]Item, []string, int64, error) {
	var items []Item
	var deleted []string
	toVersion := since
	cursor := ChangeCursor{}
	for {
		page, err := s.Source.ChangesSince(ctx, cred, since, cursor)
		if err != nil {
			return nil, nil, 0, err
		}
		items = append(items, page.Items...)
		deleted = append(deleted, page.Deleted...)
		if page.LastModifiedVersion > toVersion {
			toVersion = page.LastModifiedVersion
		}
		if page.Next == nil {
			break
		}
		cursor = *page.Next
	}
	return items, deleted, toVersion, nil
}

// stageDocuments resolves each top-level item into an indexable document.
// Fulltext is prefered over abstract, and attachment fetches run through a
// bounded worker pool (<= maxSyncConcurrency) so a rate-limited free tier still
// completes in reasonable time without exceeding the concurrency boundary.
func (s *Syncer) stageDocuments(ctx context.Context, cred Credential, items []Item) ([]stagedDocument, error) {
	slots := make([]*stagedDocument, len(items))
	jobs := make(chan int)
	errCh := make(chan error, 1)

	workers := maxSyncConcurrency
	if workers > len(items) {
		workers = len(items)
	}
	if workers < 1 {
		workers = 1
	}

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	runJob := func(item Item) (*stagedDocument, error) {
		if item.Key == "" || item.Deleted {
			return nil, nil
		}
		doc := stagedDocument{
			Key: item.Key, Version: item.Version, ItemType: item.ItemType, Title: item.Title,
			Abstract: item.Abstract, Year: item.Year, Venue: item.Venue, DOI: normalizeDOI(item.DOI),
			Authors: item.Authors, Collections: item.Collections, ZoteroVersion: item.Version,
		}
		if strings.TrimSpace(doc.ItemType) == "" {
			doc.ItemType = "journalArticle"
		}
		attachments, err := s.Source.ListAttachments(sctx, cred, item.Key)
		if err != nil {
			return nil, err
		}
		attachmentKeys := append([]string(nil), item.AttachmentKeys...)
		for _, a := range attachments {
			if a.Key != "" {
				attachmentKeys = appendUnique(attachmentKeys, a.Key)
			}
		}
		for _, key := range attachmentKeys {
			ft, err := s.Source.GetFulltext(sctx, cred, key)
			if err != nil {
				return nil, err
			}
			if ft.Available && strings.TrimSpace(ft.Content) != "" {
				doc.Fulltext = strings.TrimSpace(ft.Content)
				break
			}
		}
		switch {
		case strings.TrimSpace(doc.Fulltext) != "":
			t := domain.EvidencePersonalFulltext
			doc.State, doc.EvidenceType, doc.SourceKind = domain.IndexIndexed, &t, "fulltext"
		case strings.TrimSpace(doc.Abstract) != "":
			t := domain.EvidenceStructuredAbstract
			doc.State, doc.EvidenceType, doc.SourceKind = domain.IndexIndexed, &t, "abstract"
		default:
			doc.State, doc.EvidenceType, doc.SourceKind = domain.IndexMetadataOnly, nil, "metadata"
		}
		doc.ContentHash = documentContentHash(doc)
		return &doc, nil
	}

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for idx := range jobs {
				doc, err := runJob(items[idx])
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					cancel()
					return
				}
				slots[idx] = doc
			}
		}()
	}

dispatch:
	for i := range items {
		select {
		case <-sctx.Done():
			break dispatch
		case jobs <- i:
		}
	}
	close(jobs)
	wg.Wait()

	select {
	case err := <-errCh:
		return nil, err
	default:
	}

	out := make([]stagedDocument, 0, len(items))
	for _, doc := range slots {
		if doc != nil {
			out = append(out, *doc)
		}
	}
	return out, nil
}

const maxSyncConcurrency = 4

func (s *Syncer) upsertCollections(ctx context.Context, tx pgx.Tx, workspaceID, connectionID string, collections []Collection) (map[string]string, error) {
	ids := map[string]string{}
	for _, c := range collections {
		if c.Key == "" {
			continue
		}
		id := storage.StableID("collection", connectionID, c.Key)
		if _, err := tx.Exec(ctx, `
			INSERT INTO collections (id,workspace_id,connection_id,zotero_key,parent_key,name,version,is_deleted)
			VALUES ($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8)
			ON CONFLICT (id) DO UPDATE SET parent_key=EXCLUDED.parent_key,name=EXCLUDED.name,
				version=EXCLUDED.version,is_deleted=EXCLUDED.is_deleted,updated_at=now()
		`, id, workspaceID, connectionID, c.Key, c.ParentKey, c.Name, c.Version, c.Deleted); err != nil {
			return nil, fmt.Errorf("upsert collection %s: %w", c.Key, err)
		}
		ids[c.Key] = id
	}
	// Mark collections absent from this listing as deleted without dropping rows.
	if len(ids) > 0 {
		keys := make([]string, 0, len(ids))
		for k := range ids {
			keys = append(keys, k)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE collections SET is_deleted=true, updated_at=now()
			WHERE connection_id=$1 AND zotero_key<>ALL($2)
		`, connectionID, keys); err != nil {
			return nil, fmt.Errorf("reconcile collections: %w", err)
		}
	}
	return ids, nil
}

func (s *Syncer) upsertDocument(ctx context.Context, tx pgx.Tx, workspaceID, connectionID string, doc stagedDocument) (string, string, int, bool, error) {
	docID := storage.StableID("doc", connectionID, doc.Key)
	var evidenceValue any
	if doc.EvidenceType != nil {
		evidenceValue = string(*doc.EvidenceType)
	}
	authors, err := storage.JSON(doc.Authors)
	if err != nil {
		return "", "", 0, false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO documents
			(id,workspace_id,connection_id,zotero_item_key,item_type,title,abstract,year,venue,doi_normalized,
			 authors_json,index_status,evidence_type,zotero_version,content_hash,last_seen_at,removed_at,purge_after)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12,$13,$14,$15,$16,NULL,NULL)
		ON CONFLICT (id) DO UPDATE SET
			item_type=EXCLUDED.item_type,title=EXCLUDED.title,abstract=EXCLUDED.abstract,year=EXCLUDED.year,
			venue=EXCLUDED.venue,doi_normalized=EXCLUDED.doi_normalized,authors_json=EXCLUDED.authors_json,
			index_status=EXCLUDED.index_status,evidence_type=EXCLUDED.evidence_type,zotero_version=EXCLUDED.zotero_version,
			last_seen_at=EXCLUDED.last_seen_at,updated_at=EXCLUDED.last_seen_at,removed_at=NULL,purge_after=NULL
	`, docID, workspaceID, connectionID, doc.Key, doc.ItemType, doc.Title, doc.Abstract, nullableInt(doc.Year),
		doc.Venue, doc.DOI, authors, doc.State, evidenceValue, doc.ZoteroVersion, doc.ContentHash,
		s.now()); err != nil {
		return "", "", 0, false, fmt.Errorf("upsert document %s: %w", doc.Key, err)
	}

	var latestID string
	var latestNo int
	var latestHash string
	err = tx.QueryRow(ctx, `
		SELECT id, version_no, content_hash FROM document_versions
		WHERE document_id=$1 ORDER BY version_no DESC LIMIT 1
	`, docID).Scan(&latestID, &latestNo, &latestHash)
	if err != nil && err != pgx.ErrNoRows {
		return "", "", 0, false, err
	}
	added := false
	versionID := latestID
	versionNo := latestNo
	if latestID == "" || latestHash != doc.ContentHash {
		versionNo = latestNo + 1
		versionID = storage.StableID("dv", docID, fmt.Sprint(versionNo), doc.ContentHash)
		if _, err := tx.Exec(ctx, `
			INSERT INTO document_versions
				(id,document_id,workspace_id,version_no,source_kind,fulltext,abstract,content_hash,published_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		`, versionID, docID, workspaceID, versionNo, doc.SourceKind, doc.Fulltext, doc.Abstract, doc.ContentHash, s.now()); err != nil {
			return "", "", 0, false, fmt.Errorf("insert document version %s: %w", doc.Key, err)
		}
		added = true
	}
	if _, err := tx.Exec(ctx, `UPDATE documents SET active_version_id=$2, content_hash=$3 WHERE id=$1`, docID, versionID, doc.ContentHash); err != nil {
		return "", "", 0, false, err
	}
	return docID, versionID, versionNo, added, nil
}

func (s *Syncer) tombstoneDocument(ctx context.Context, tx pgx.Tx, workspaceID, connectionID, key string, now time.Time) (int, error) {
	docID := storage.StableID("doc", connectionID, key)
	var existing string
	err := tx.QueryRow(ctx, `SELECT id FROM documents WHERE id=$1 AND workspace_id=$2`, docID, workspaceID).Scan(&existing)
	if err != nil {
		if err == pgx.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE documents SET removed_at=$3, purge_after=$4, updated_at=$3
		WHERE id=$1 AND workspace_id=$2 AND removed_at IS NULL
	`, docID, workspaceID, now, now.Add(tombstoneRetention))
	if err != nil {
		return 0, fmt.Errorf("tombstone document %s: %w", key, err)
	}
	if tag.RowsAffected() == 0 {
		return 0, nil
	}
	tombstoneID := storage.StableID("tombstone", workspaceID, "document", docID)
	if _, err := tx.Exec(ctx, `
		INSERT INTO lifecycle_tombstones (id,workspace_id,object_type,object_id,reason,effective_at,purge_after,status)
		VALUES ($1,$2,'document',$3,'zotero_item_deleted',$4,$5,'active')
		ON CONFLICT (id) DO UPDATE SET effective_at=EXCLUDED.effective_at,purge_after=EXCLUDED.purge_after,status='active'
	`, tombstoneID, workspaceID, docID, now, now.Add(tombstoneRetention)); err != nil {
		return 0, fmt.Errorf("write tombstone %s: %w", key, err)
	}
	return 1, nil
}

func (s *Syncer) activeEpoch(ctx context.Context, tx pgx.Tx, workspaceID string) (string, string) {
	var id, hash string
	if err := tx.QueryRow(ctx, `
		SELECT id, content_hash FROM index_epochs
		WHERE workspace_id=$1 AND status='active' AND retired_at IS NULL
		ORDER BY published_at DESC NULLS LAST LIMIT 1
	`, workspaceID).Scan(&id, &hash); err != nil {
		return "", ""
	}
	return id, hash
}

func (s *Syncer) finishRun(ctx context.Context, tx pgx.Tx, in persistInput, counts struct{ fulltext, abstracts, metadataOnly int }, versionsAdded, deleted, chunks int, status string, runErr error) error {
	var code, message any
	if runErr != nil {
		code, message = "sync_failed", runErr.Error()
	}
	counters, _ := storage.JSON(map[string]int{
		"documents": len(in.staged), "versions_added": versionsAdded, "chunks": chunks,
		"fulltext": counts.fulltext, "abstracts": counts.abstracts, "metadata_only": counts.metadataOnly,
		"deleted": deleted,
	})
	if _, err := tx.Exec(ctx, `
		UPDATE zotero_sync_runs
		SET status=$2, counters_jsonb=$3::jsonb, error_code=$4, error_message=$5, finished_at=$6
		WHERE id=$1
	`, in.runID, status, counters, code, message, in.now); err != nil {
		return fmt.Errorf("finish sync run: %w", err)
	}
	return nil
}

func (s *Syncer) noopResult(ctx context.Context, workspaceID string, mode SyncMode) SyncResult {
	active, _ := s.Store.ActiveEpochID(ctx, workspaceID)
	var docs, fulltext, abstracts, metadataOnly int
	_ = s.Store.Pool().QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE evidence_type='personal_fulltext'),
		       count(*) FILTER (WHERE evidence_type='structured_abstract'),
		       count(*) FILTER (WHERE index_status='metadata_only')
		FROM documents WHERE workspace_id=$1 AND removed_at IS NULL
	`, workspaceID).Scan(&docs, &fulltext, &abstracts, &metadataOnly)
	return SyncResult{
		EpochID: active, NewEpoch: false, Documents: docs,
		Fulltext: fulltext, Abstracts: abstracts, MetadataOnly: metadataOnly,
	}
}

func coverageJSON(counts struct{ fulltext, abstracts, metadataOnly int }, deleted int) string {
	b, _ := storage.JSON(map[string]int{
		"fulltext": counts.fulltext, "abstract": counts.abstracts,
		"metadata_only": counts.metadataOnly, "deleted": deleted,
	})
	return b
}

func documentContentHash(doc stagedDocument) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		fmt.Sprint(doc.ZoteroVersion), doc.Key, doc.Title, doc.Abstract, fmt.Sprint(doc.Year),
		doc.Venue, normalizeDOI(doc.DOI), doc.ItemType, doc.Fulltext, doc.SourceKind,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func hashStrings(values []string) string {
	if len(values) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(values, "\x01")))
	return hex.EncodeToString(sum[:])
}

func appendUnique(list []string, value string) []string {
	for _, v := range list {
		if v == value {
			return list
		}
	}
	return append(list, value)
}

// buildDocumentChunks indexes fulltext when present, otherwise the abstract.
// A document never contributes both, matching the evidence precedence rule.
func buildDocumentChunks(epochID, documentID, versionID, fulltext, abstract, sourceKind string, dim int, embeddingVersion string) []stagedChunk {
	type chunkSource struct {
		section string
		text    string
	}
	var sources []chunkSource
	if strings.TrimSpace(fulltext) != "" {
		for idx, text := range splitText(fulltext, 1800) {
			sources = append(sources, chunkSource{section: fmt.Sprintf("body-%d", idx+1), text: text})
		}
	} else if strings.TrimSpace(abstract) != "" {
		sources = append(sources, chunkSource{section: "abstract", text: strings.TrimSpace(abstract)})
	}
	out := make([]stagedChunk, 0, len(sources))
	stub := embedding.Stub{Dim: dim}
	corpus := fulltext + abstract
	for idx, src := range sources {
		start := strings.Index(corpus, src.text)
		if start < 0 {
			start = 0
		}
		end := start + len(src.text)
		chunk := domain.Chunk{IndexEpochID: epochID, DocumentVersionID: versionID, Ordinal: idx}
		out = append(out, stagedChunk{
			ID: chunkKey(chunk), Ordinal: idx, Section: src.section, CharStart: start, CharEnd: end,
			Content: src.text, TokenCount: len(strings.Fields(src.text)), Vector: stub.Vector(src.text),
		})
	}
	return out
}
