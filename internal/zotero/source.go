package zotero

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

const defaultPageLimit = 100

// Credential is the short-lived, decrypted credential passed to a Zotero source.
// It must not be logged or persisted outside the credential store boundary.
type Credential struct {
	APIKey      string
	LibraryType string
	LibraryID   string
}

type ConnectionInfo struct {
	UserID      string
	Username    string
	LibraryID   string
	LibraryType string
}

type Collection struct {
	Key       string
	Version   int64
	Name      string
	ParentKey string
	Deleted   bool
}

type CollectionCursor struct {
	Start    int
	PageSize int
}

type CollectionPage struct {
	Items               []Collection
	Next                *CollectionCursor
	LastModifiedVersion int64
}

type Item struct {
	Key            string
	Version        int64
	ItemType       string
	Title          string
	Abstract       string
	Year           int
	Venue          string
	DOI            string
	Authors        []string
	Collections    []string
	AttachmentKeys []string
	Deleted        bool
}

type Attachment struct {
	Key         string
	ContentType string
	Filename    string
}

type ChangeCursor struct {
	Start    int
	PageSize int
}

type ChangePage struct {
	Items               []Item
	Deleted             []string
	Next                *ChangeCursor
	LastModifiedVersion int64
}

type Fulltext struct {
	Available bool
	Content   string
}

type SourceError struct {
	Code      string
	Retryable bool
	Status    int
	Cause     error
}

func (e *SourceError) Error() string {
	if e == nil {
		return ""
	}
	if e.Status != 0 {
		return fmt.Sprintf("zotero source error: %s (status %d)", e.Code, e.Status)
	}
	return "zotero source error: " + e.Code
}

func (e *SourceError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type Source interface {
	Validate(ctx context.Context, credential Credential) (ConnectionInfo, error)
	ListCollections(ctx context.Context, credential Credential, cursor CollectionCursor) (CollectionPage, error)
	GetLibraryVersion(ctx context.Context, credential Credential) (int64, error)
	ChangesSince(ctx context.Context, credential Credential, version int64, cursor ChangeCursor) (ChangePage, error)
	GetItem(ctx context.Context, credential Credential, itemKey string) (Item, error)
	ListAttachments(ctx context.Context, credential Credential, itemKey string) ([]Attachment, error)
	GetFulltext(ctx context.Context, credential Credential, attachmentKey string) (Fulltext, error)
}

type WebAPISource struct {
	baseURL    string
	apiVersion string
	client     *http.Client
	sleep      func(context.Context, time.Duration) error
	maxRetries int
}

func NewWebAPISource(baseURL, apiVersion string, client *http.Client) *WebAPISource {
	if strings.TrimSpace(apiVersion) == "" {
		apiVersion = "3"
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &WebAPISource{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiVersion: apiVersion,
		client:     client,
		sleep:      sleepContext,
		maxRetries: 5,
	}
}

// NewHTTPClient builds the shared HTTP client used for Zotero Web API calls.
// Concurrency reported by the caller is advisory: Go's transport pools
// connections, and the syncer issues requests sequentially.
func NewHTTPClient(concurrency int) *http.Client {
	if concurrency < 1 {
		concurrency = 4
	}
	return &http.Client{
		Timeout: 45 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        concurrency,
			MaxIdleConnsPerHost: concurrency,
			MaxConnsPerHost:     concurrency,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *WebAPISource) Validate(ctx context.Context, credential Credential) (ConnectionInfo, error) {
	status, _, body, err := s.request(ctx, credential, http.MethodGet, "/keys/current", nil)
	if err != nil {
		return ConnectionInfo{}, err
	}
	if err := statusError(status); err != nil {
		return ConnectionInfo{}, err
	}
	var payload struct {
		UserID   any    `json:"userID"`
		Username string `json:"username"`
		Key      string `json:"key"`
		Access   any    `json:"access"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ConnectionInfo{}, &SourceError{Code: "invalid_response", Cause: err}
	}
	userID := scalarString(payload.UserID)
	if userID == "" {
		return ConnectionInfo{}, &SourceError{Code: "invalid_response"}
	}
	libraryType := normalizeLibraryType(credential.LibraryType)
	libraryID := strings.TrimSpace(credential.LibraryID)
	if libraryID == "" {
		libraryID = userID
	}
	return ConnectionInfo{UserID: userID, Username: strings.TrimSpace(payload.Username), LibraryID: libraryID, LibraryType: libraryType}, nil
}

func (s *WebAPISource) ListCollections(ctx context.Context, credential Credential, cursor CollectionCursor) (CollectionPage, error) {
	query := url.Values{}
	query.Set("start", fmt.Sprint(cursor.Start))
	query.Set("limit", fmt.Sprint(defaultPageLimit))
	status, headers, body, err := s.request(ctx, credential, http.MethodGet, s.libraryPath(credential, "/collections"), query)
	if err != nil {
		return CollectionPage{}, err
	}
	if err := statusError(status); err != nil {
		return CollectionPage{}, err
	}
	var raw []collectionEnvelope
	if err := json.Unmarshal(body, &raw); err != nil {
		return CollectionPage{}, &SourceError{Code: "invalid_response", Cause: err}
	}
	items := make([]Collection, 0, len(raw))
	for _, entry := range raw {
		items = append(items, normalizeCollection(entry))
	}
	next := nextCollectionCursor(cursor, len(items), headerInt64(headers, "Total-Results"))
	return CollectionPage{Items: items, Next: next, LastModifiedVersion: headerInt64(headers, "Last-Modified-Version")}, nil
}

func (s *WebAPISource) GetLibraryVersion(ctx context.Context, credential Credential) (int64, error) {
	status, headers, _, err := s.request(ctx, credential, http.MethodGet, s.libraryPath(credential, "/items/top"), url.Values{"limit": {"1"}})
	if err != nil {
		return 0, err
	}
	if err := statusError(status); err != nil {
		return 0, err
	}
	return headerInt64(headers, "Last-Modified-Version"), nil
}

func (s *WebAPISource) ChangesSince(ctx context.Context, credential Credential, version int64, cursor ChangeCursor) (ChangePage, error) {
	query := url.Values{}
	query.Set("start", fmt.Sprint(cursor.Start))
	query.Set("limit", fmt.Sprint(defaultPageLimit))
	query.Set("since", fmt.Sprint(version))
	status, headers, body, err := s.request(ctx, credential, http.MethodGet, s.libraryPath(credential, "/items/top"), query)
	if err != nil {
		return ChangePage{}, err
	}
	if err := statusError(status); err != nil {
		return ChangePage{}, err
	}
	var raw []itemEnvelope
	if err := json.Unmarshal(body, &raw); err != nil {
		return ChangePage{}, &SourceError{Code: "invalid_response", Cause: err}
	}
	items := make([]Item, 0, len(raw))
	deleted := make([]string, 0)
	for _, entry := range raw {
		item, isDeleted := normalizeItemEnvelope(entry)
		if isDeleted {
			if item.Key != "" {
				deleted = append(deleted, item.Key)
			}
			continue
		}
		if item.Key != "" {
			items = append(items, item)
		}
	}
	next := nextChangeCursor(cursor, len(items)+len(deleted), headerInt64(headers, "Total-Results"))
	return ChangePage{Items: items, Deleted: deleted, Next: next, LastModifiedVersion: headerInt64(headers, "Last-Modified-Version")}, nil
}

func (s *WebAPISource) GetItem(ctx context.Context, credential Credential, itemKey string) (Item, error) {
	if strings.TrimSpace(itemKey) == "" {
		return Item{}, &SourceError{Code: "not_found"}
	}
	status, _, body, err := s.request(ctx, credential, http.MethodGet, s.libraryPath(credential, "/items/"+url.PathEscape(itemKey)), nil)
	if err != nil {
		return Item{}, err
	}
	if err := statusError(status); err != nil {
		return Item{}, err
	}
	var raw itemEnvelope
	if err := json.Unmarshal(body, &raw); err != nil {
		// Some compatible endpoints return a one-element array.
		var list []itemEnvelope
		if listErr := json.Unmarshal(body, &list); listErr != nil || len(list) == 0 {
			return Item{}, &SourceError{Code: "invalid_response", Cause: err}
		}
		raw = list[0]
	}
	item, deleted := normalizeItemEnvelope(raw)
	if deleted {
		return Item{}, &SourceError{Code: "not_found"}
	}
	return item, nil
}

func (s *WebAPISource) ListAttachments(ctx context.Context, credential Credential, itemKey string) ([]Attachment, error) {
	if strings.TrimSpace(itemKey) == "" {
		return nil, nil
	}
	query := url.Values{"itemType": {"attachment"}}
	status, _, body, err := s.request(ctx, credential, http.MethodGet, s.libraryPath(credential, "/items/"+url.PathEscape(itemKey)+"/children"), query)
	if err != nil {
		return nil, err
	}
	if err := statusError(status); err != nil {
		return nil, err
	}
	var raw []itemEnvelope
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, &SourceError{Code: "invalid_response", Cause: err}
	}
	out := make([]Attachment, 0, len(raw))
	for _, entry := range raw {
		item, deleted := normalizeItemEnvelope(entry)
		if deleted || item.Key == "" {
			continue
		}
		contentType := strings.TrimSpace(entry.Data.ContentType)
		if contentType == "" {
			contentType = strings.TrimSpace(entry.Data.MIMEType)
		}
		out = append(out, Attachment{Key: item.Key, ContentType: contentType, Filename: strings.TrimSpace(entry.Data.Filename)})
	}
	return out, nil
}

func (s *WebAPISource) GetFulltext(ctx context.Context, credential Credential, attachmentKey string) (Fulltext, error) {
	if strings.TrimSpace(attachmentKey) == "" {
		return Fulltext{}, nil
	}
	status, _, body, err := s.request(ctx, credential, http.MethodGet, s.libraryPath(credential, "/items/"+url.PathEscape(attachmentKey)+"/fulltext"), nil)
	if err != nil {
		return Fulltext{}, err
	}
	if status == http.StatusNotFound {
		return Fulltext{}, nil
	}
	if err := statusError(status); err != nil {
		return Fulltext{}, err
	}
	var payload struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		// A few API deployments return raw text rather than the documented object.
		if len(strings.TrimSpace(string(body))) == 0 {
			return Fulltext{}, nil
		}
		return Fulltext{Available: true, Content: strings.TrimSpace(string(body))}, nil
	}
	if strings.TrimSpace(payload.Content) == "" {
		return Fulltext{}, nil
	}
	return Fulltext{Available: true, Content: strings.TrimSpace(payload.Content)}, nil
}

func (s *WebAPISource) request(ctx context.Context, credential Credential, method, requestPath string, query url.Values) (int, http.Header, []byte, error) {
	if strings.TrimSpace(credential.APIKey) == "" {
		return 0, nil, nil, &SourceError{Code: "unauthorized", Status: http.StatusUnauthorized}
	}
	endpoint := s.baseURL + requestPath
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var lastErr error
	for attempt := 0; attempt <= s.maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
		if err != nil {
			return 0, nil, nil, &SourceError{Code: "request_failed", Cause: err}
		}
		req.Header.Set("Zotero-API-Key", credential.APIKey)
		req.Header.Set("Zotero-API-Version", s.apiVersion)
		req.Header.Set("User-Agent", "research-scout/0.1 (stage1-zotero-sync)")
		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = &SourceError{Code: "request_failed", Retryable: true, Cause: err}
			if attempt == s.maxRetries {
				return 0, nil, nil, lastErr
			}
			if sleepErr := s.sleep(ctx, exponentialBackoff(attempt)); sleepErr != nil {
				return 0, nil, nil, sleepErr
			}
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return resp.StatusCode, resp.Header, nil, &SourceError{Code: "response_read_failed", Retryable: true, Cause: readErr}
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			if attempt == s.maxRetries {
				code := "rate_limited"
				if resp.StatusCode >= 500 {
					code = "upstream_unavailable"
				}
				return resp.StatusCode, resp.Header, body, &SourceError{Code: code, Retryable: true, Status: resp.StatusCode}
			}
			delay := retryAfter(resp.Header.Get("Retry-After"), exponentialBackoff(attempt))
			if err := s.sleep(ctx, delay); err != nil {
				return 0, nil, nil, err
			}
			continue
		}
		return resp.StatusCode, resp.Header, body, nil
	}
	return 0, nil, nil, lastErr
}

func (s *WebAPISource) libraryPath(credential Credential, suffix string) string {
	libraryType := normalizeLibraryType(credential.LibraryType)
	libraryID := strings.TrimSpace(credential.LibraryID)
	return "/" + libraryType + "s/" + url.PathEscape(libraryID) + suffix
}

func normalizeLibraryType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "group":
		return "group"
	default:
		return "user"
	}
}

func statusError(status int) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &SourceError{Code: "unauthorized", Status: status}
	case status == http.StatusNotFound:
		return &SourceError{Code: "not_found", Status: status}
	case status == http.StatusTooManyRequests:
		return &SourceError{Code: "rate_limited", Retryable: true, Status: status}
	case status >= 500:
		return &SourceError{Code: "upstream_unavailable", Retryable: true, Status: status}
	default:
		return &SourceError{Code: "request_failed", Status: status}
	}
}

func retryAfter(value string, fallback time.Duration) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if seconds, err := time.ParseDuration(value + "s"); err == nil {
		if seconds < 0 {
			return 0
		}
		return seconds
	}
	if when, err := http.ParseTime(value); err == nil {
		d := time.Until(when)
		if d < 0 {
			return 0
		}
		return d
	}
	return fallback
}

func exponentialBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(250*(1<<attempt)) * time.Millisecond
}

func headerInt64(headers http.Header, key string) int64 {
	if headers == nil {
		return 0
	}
	var value int64
	if _, err := fmt.Sscan(strings.TrimSpace(headers.Get(key)), &value); err != nil {
		return 0
	}
	return value
}

func nextCollectionCursor(cursor CollectionCursor, count int, total int64) *CollectionCursor {
	if count == 0 {
		return nil
	}
	pageSize := cursor.PageSize
	if pageSize <= 0 {
		pageSize = count
	}
	nextStart := cursor.Start + count
	if total > 0 && int64(nextStart) >= total {
		return nil
	}
	if pageSize > 0 && count < pageSize {
		return nil
	}
	return &CollectionCursor{Start: nextStart, PageSize: pageSize}
}

func nextChangeCursor(cursor ChangeCursor, count int, total int64) *ChangeCursor {
	if count == 0 {
		return nil
	}
	pageSize := cursor.PageSize
	if pageSize <= 0 {
		pageSize = count
	}
	nextStart := cursor.Start + count
	// Zotero's change feed can return a stale/partial Total-Results value. The
	// observed page size is the safe cursor signal; one empty probe terminates it.
	if total > 0 && int64(nextStart) >= total && pageSize != count {
		return nil
	}
	if count < pageSize {
		return nil
	}
	return &ChangeCursor{Start: nextStart, PageSize: pageSize}
}

type collectionEnvelope struct {
	Key     string `json:"key"`
	Version int64  `json:"version"`
	Data    struct {
		Key              string `json:"key"`
		Name             string `json:"name"`
		ParentCollection any    `json:"parentCollection"`
		Deleted          any    `json:"deleted"`
	} `json:"data"`
}

func normalizeCollection(entry collectionEnvelope) Collection {
	key := firstNonEmpty(entry.Key, entry.Data.Key)
	return Collection{
		Key: key, Version: entry.Version, Name: strings.TrimSpace(entry.Data.Name),
		ParentKey: scalarString(entry.Data.ParentCollection), Deleted: truthy(entry.Data.Deleted),
	}
}

type itemEnvelope struct {
	Key     string      `json:"key"`
	Version int64       `json:"version"`
	Data    itemData    `json:"data"`
	Links   itemLinks   `json:"links"`
	Deleted any         `json:"deleted"`
	Extra   interface{} `json:"-"`
}

type itemData struct {
	Key              string          `json:"key"`
	ItemType         string          `json:"itemType"`
	Title            string          `json:"title"`
	Abstract         string          `json:"abstractNote"`
	Date             string          `json:"date"`
	PublicationTitle string          `json:"publicationTitle"`
	ConferenceName   string          `json:"conferenceName"`
	ProceedingsTitle string          `json:"proceedingsTitle"`
	BookTitle        string          `json:"bookTitle"`
	DOI              string          `json:"DOI"`
	DOI2             string          `json:"doi"`
	Collections      []string        `json:"collections"`
	Creators         []creator       `json:"creators"`
	Deleted          any             `json:"deleted"`
	ContentType      string          `json:"contentType"`
	MIMEType         string          `json:"mimeType"`
	Filename         string          `json:"filename"`
	Attachments      json.RawMessage `json:"attachments"`
}

type creator struct {
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Name      string `json:"name"`
}

type itemLinks struct {
	Attachment any `json:"attachment"`
}

func normalizeItemEnvelope(entry itemEnvelope) (Item, bool) {
	key := firstNonEmpty(entry.Key, entry.Data.Key)
	deleted := truthy(entry.Data.Deleted) || truthy(entry.Deleted)
	item := Item{
		Key: key, Version: entry.Version, ItemType: strings.TrimSpace(entry.Data.ItemType),
		Title: cleanText(entry.Data.Title), Abstract: cleanText(entry.Data.Abstract),
		Year: yearFromDate(entry.Data.Date), Venue: firstNonEmpty(entry.Data.PublicationTitle, entry.Data.ConferenceName, entry.Data.ProceedingsTitle, entry.Data.BookTitle),
		DOI: normalizeDOI(firstNonEmpty(entry.Data.DOI, entry.Data.DOI2)), Collections: append([]string(nil), entry.Data.Collections...),
		Authors: creatorsToAuthors(entry.Data.Creators), Deleted: deleted,
	}
	item.AttachmentKeys = attachmentKeys(entry.Links.Attachment)
	return item, deleted
}

func creatorsToAuthors(creators []creator) []string {
	out := make([]string, 0, len(creators))
	for _, c := range creators {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			name = strings.TrimSpace(strings.TrimSpace(c.FirstName) + " " + strings.TrimSpace(c.LastName))
		}
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func attachmentKeys(value any) []string {
	switch v := value.(type) {
	case string:
		if key := keyFromURL(v); key != "" {
			return []string{key}
		}
	case map[string]any:
		if href, ok := v["href"].(string); ok {
			if key := keyFromURL(href); key != "" {
				return []string{key}
			}
		}
	case []any:
		var out []string
		for _, entry := range v {
			out = append(out, attachmentKeys(entry)...)
		}
		return out
	}
	return nil
}

func keyFromURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	key := path.Base(strings.TrimRight(parsed.Path, "/"))
	if key == "." || key == "/" {
		return ""
	}
	return key
}

func yearFromDate(value string) int {
	match := regexp.MustCompile(`(?:19|20)\d{2}`).FindString(value)
	if match == "" {
		return 0
	}
	var year int
	_, _ = fmt.Sscan(match, &year)
	return year
}

func cleanText(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\x00' || (r < 0x20 && r != '\n' && r != '\r' && r != '\t') {
			return -1
		}
		return r
	}, value)
	return strings.TrimSpace(value)
}

func scalarString(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return strings.TrimSpace(v.String())
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprint(v)
	case int:
		return fmt.Sprint(v)
	case int64:
		return fmt.Sprint(v)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func truthy(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return v == "1" || strings.EqualFold(v, "true")
	case float64:
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
