package domain

import "time"

type RetrievalMode string

const (
	RetrievalModeMyLibrary RetrievalMode = "my_library"
	RetrievalModeGapFill   RetrievalMode = "gap_fill"
	RetrievalModeLandscape RetrievalMode = "landscape"
)

type EvidenceType string

const (
	EvidencePersonalFulltext   EvidenceType = "personal_fulltext"
	EvidenceStructuredAbstract EvidenceType = "structured_abstract"
	EvidenceWebContext         EvidenceType = "web_context"
)

type IndexStatus string

const (
	IndexPending       IndexStatus = "pending"
	IndexIndexed       IndexStatus = "indexed"
	IndexMetadataOnly  IndexStatus = "metadata_only"
	IndexFailed        IndexStatus = "failed"
	IndexSkipped       IndexStatus = "skipped"
)

type IndexEpochStatus string

const (
	EpochBuilding IndexEpochStatus = "building"
	EpochActive   IndexEpochStatus = "active"
	EpochRetired  IndexEpochStatus = "retired"
)

type ClaimType string

const (
	ClaimSummary    ClaimType = "summary"
	ClaimFinding    ClaimType = "finding"
	ClaimMethod     ClaimType = "method"
	ClaimChronology ClaimType = "chronology"
)

type ClaimStance string

const (
	ClaimSupports   ClaimStance = "supports"
	ClaimRefutes    ClaimStance = "refutes"
	ClaimBackground ClaimStance = "background"
)

type VerificationStatus string

const (
	VerificationPending  VerificationStatus = "pending"
	VerificationVerified VerificationStatus = "verified"
	VerificationPartial  VerificationStatus = "partial"
	VerificationRejected VerificationStatus = "rejected"
)

type SourceStatus string

const (
	SourceAvailable   SourceStatus = "available"
	SourceDegraded    SourceStatus = "degraded"
	SourceUnavailable SourceStatus = "unavailable"
	SourceSkipped     SourceStatus = "skipped"
)

type QueryPhase string

const (
	PhaseAccepted            QueryPhase = "accepted"
	PhasePlanning            QueryPhase = "planning"
	PhaseRetrievingLibrary   QueryPhase = "retrieving_library"
	PhaseRetrievingScholarly QueryPhase = "retrieving_scholarly"
	PhaseRetrievingWeb       QueryPhase = "retrieving_web"
	PhaseRanking             QueryPhase = "ranking"
	PhaseDrafting            QueryPhase = "drafting"
	PhaseVerifying           QueryPhase = "verifying"
	PhaseCompleted           QueryPhase = "completed"
)

type Document struct {
	ID             string
	WorkspaceID    string
	ZoteroItemKey  string
	ItemType       string
	Title          string
	Abstract       string
	Year           int
	Venue          string
	DOINormalized  string
	ActiveVersionID string
	IndexStatus    IndexStatus
	EvidenceType   *EvidenceType
	ContentHash    string
	RemovedAt      *time.Time
}

type DocumentVersion struct {
	ID           string
	DocumentID   string
	WorkspaceID  string
	VersionNo    int
	SourceKind   string
	Fulltext     string
	Abstract     string
	ContentHash  string
	PublishedAt  time.Time
}

type Chunk struct {
	ID               string
	IndexEpochID     string
	DocumentVersionID string
	DocumentID       string
	WorkspaceID      string
	Ordinal          int
	Section          string
	PageStart        *int
	PageEnd          *int
	CharStart        int
	CharEnd          int
	Content          string
	TokenCount       int
	EmbeddingVersion string
	Score            float64
}

type Evidence struct {
	ID               string
	BriefID          string
	WorkspaceID      string
	IndexEpochID     string
	EvidenceType     EvidenceType
	DocumentID       string
	DocumentVersionID string
	ChunkID          string
	Title            string
	URL              string
	Quote            string
	Locator          map[string]any
	Score            float64
	RetrievedAt      time.Time
}

type ClaimEvidence struct {
	ClaimID       string
	EvidenceID    string
	WorkspaceID   string
	Stance        ClaimStance
	SupportScore  float64
	EvidenceSpan  string
	Locator       map[string]any
}

type Claim struct {
	ID                 string
	BriefID            string
	WorkspaceID        string
	ClaimNo            int
	ClaimType          ClaimType
	Text               string
	SupportLevel       string
	Confidence         float64
	VerificationStatus VerificationStatus
	Evidence           []ClaimEvidence
}

type Brief struct {
	ID                     string
	SessionID              string
	TurnID                 string
	WorkspaceID            string
	SchemaVersion          string
	IndexEpochID           string
	RetrievalConfigVersion string
	PromptVersion          string
	Summary                string
	SummaryClaims          []Claim
	Findings               []Claim
	OpenQuestions          []string
	SourceStatus           map[string]SourceStatus
	CreatedAt              time.Time
}

type QueryEvent struct {
	ID        string
	SessionID string
	Seq       int64
	Type      string
	At        time.Time
	Data      map[string]any
}
