package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/threave-io/threave/internal/agents"
	"github.com/threave-io/threave/internal/agents/fake"
	eventservice "github.com/threave-io/threave/internal/events"
	"github.com/threave-io/threave/internal/scheduler"
	runcontrol "github.com/threave-io/threave/internal/session"
	"github.com/threave-io/threave/internal/store"
)

func TestSessionReadsExposeOnlyFinishedTurnActivity(t *testing.T) {
	ctx := context.Background()
	database, events, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session, err := database.CreateSession(ctx, store.CreateSessionParams{Title: "Activity", AgentType: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	var want *string
	var lastSeq int64
	for _, eventType := range []string{
		"user.message.completed", "agent.message.delta", "agent.message.completed", "agent.run.completed",
		"agent.run.started", "agent.message.delta", "tool.call.completed", "agent.run.failed", "agent.run.cancelled",
	} {
		event, err := events.Append(ctx, eventservice.AppendParams{
			SessionID: session.ID, Type: eventType, Role: "assistant", Status: store.EventStatusCompleted,
			Payload: json.RawMessage(`{"text":"Work"}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if eventType != "agent.message.delta" {
			lastSeq = event.Seq
		}
		switch eventType {
		case "agent.run.completed", "agent.run.failed", "agent.run.cancelled":
			formatted := event.CreatedAt.UTC().Format(time.RFC3339Nano)
			want = &formatted
		}
		for _, read := range []*httptest.ResponseRecorder{
			get(handler, "/api/sessions/"+session.ID),
			postJSON(handler, "/api/sessions/"+session.ID+"/notification-attention/clear", `{}`),
		} {
			if read.Code != http.StatusOK {
				t.Fatalf("read activity: %d %s", read.Code, read.Body.String())
			}
			var response sessionResponse
			decodeJSON(t, read, &response)
			if !reflect.DeepEqual(response.LastActivityAt, want) {
				t.Fatalf("%s: expected finished turn activity %v, got %v", eventType, want, response.LastActivityAt)
			}
			if response.LastEventSeq != lastSeq {
				t.Fatalf("%s: expected durable cursor %d, got %d", eventType, lastSeq, response.LastEventSeq)
			}
		}
	}
}

func TestCreateSessionCreatesIdleFakeAgentSession(t *testing.T) {
	ctx := context.Background()
	workspace := canonicalPath(t, t.TempDir())
	dbStore, _, _, handler := newIntegrationAPIWithWorkdir(t, ctx, workspace, fake.New())

	rec := postJSON(handler, "/api/sessions", `{"agent_type":"fake","title":"Inspect repository"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusCreated, rec.Code, rec.Body.String())
	}

	var response createSessionResponse
	decodeJSON(t, rec, &response)
	if response.SessionID == "" {
		t.Fatal("expected session_id")
	}

	session, err := dbStore.GetSession(ctx, response.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.AgentType != "fake" {
		t.Fatalf("expected fake agent type, got %q", session.AgentType)
	}
	if session.Title != "Inspect repository" {
		t.Fatalf("expected title Inspect repository, got %q", session.Title)
	}
	if session.Status != store.SessionStatusIdle {
		t.Fatalf("expected idle status, got %q", session.Status)
	}
	if session.WorkspacePath != workspace {
		t.Fatalf("expected workspace path %q, got %q", workspace, session.WorkspacePath)
	}
}

func TestCreateSessionCreatesBlankChildWithInheritedConfiguration(t *testing.T) {
	ctx := context.Background()
	workspace := canonicalPath(t, t.TempDir())
	dbStore, _, _, handler := newIntegrationAPIWithWorkdir(t, ctx, workspace, fake.New())
	parent, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title: "Parent", AgentType: "fake", WorkspacePath: workspace,
		AgentOptions: json.RawMessage(`{"fake":{"marker":"inherited"}}`),
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}

	rec := postJSON(handler, "/api/sessions", `{"parent_session_id":`+quoteJSON(parent.ID)+`}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusCreated, rec.Code, rec.Body.String())
	}
	var response createSessionResponse
	decodeJSON(t, rec, &response)
	child, err := dbStore.GetSession(ctx, response.SessionID)
	if err != nil {
		t.Fatalf("get child: %v", err)
	}
	if child.ParentSessionID != parent.ID || child.LineageDepth != 1 {
		t.Fatalf("unexpected child lineage: %#v", child)
	}
	if child.AgentType != parent.AgentType || child.WorkspacePath != parent.WorkspacePath {
		t.Fatalf("expected inherited agent and workspace, got %#v", child)
	}
	if string(child.AgentOptions) != string(parent.AgentOptions) {
		t.Fatalf("expected inherited options %s, got %s", parent.AgentOptions, child.AgentOptions)
	}
	if child.Title != "" || child.EventCount != 0 || child.Status != store.SessionStatusIdle {
		t.Fatalf("expected blank idle child, got %#v", child)
	}
}

func TestCreateSessionRejectsArchivedParent(t *testing.T) {
	ctx := context.Background()
	workspace := canonicalPath(t, t.TempDir())
	dbStore, _, _, handler := newIntegrationAPIWithWorkdir(t, ctx, workspace, fake.New())
	parent, err := dbStore.CreateSession(ctx, store.CreateSessionParams{AgentType: "fake", WorkspacePath: workspace})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if _, err := dbStore.ArchiveSession(ctx, store.ArchiveSessionParams{ID: parent.ID}); err != nil {
		t.Fatalf("archive parent: %v", err)
	}

	rec := postJSON(handler, "/api/sessions", `{"parent_session_id":`+quoteJSON(parent.ID)+`}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusConflict, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "parent session is archived")
}

func TestCreateSessionAcceptsWorkspaceInsideAllowedRoot(t *testing.T) {
	ctx := context.Background()
	root := canonicalPath(t, t.TempDir())
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatalf("create project directory: %v", err)
	}
	dbStore, _, _, handler := newIntegrationAPIWithWorkdir(t, ctx, root, fake.New())

	rec := postJSON(handler, "/api/sessions", `{"agent_type":"fake","workspace_path":`+quoteJSON(project)+`}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusCreated, rec.Code, rec.Body.String())
	}
	var response createSessionResponse
	decodeJSON(t, rec, &response)
	session, err := dbStore.GetSession(ctx, response.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.WorkspacePath != project {
		t.Fatalf("expected workspace path %q, got %q", project, session.WorkspacePath)
	}
}

func TestCreateSessionRejectsWorkspaceOutsideAllowedRoots(t *testing.T) {
	ctx := context.Background()
	root := canonicalPath(t, t.TempDir())
	outside := canonicalPath(t, t.TempDir())
	_, _, _, handler := newIntegrationAPIWithWorkdir(t, ctx, root, fake.New())

	rec := postJSON(handler, "/api/sessions", `{"agent_type":"fake","workspace_path":`+quoteJSON(outside)+`}`)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusForbidden, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "workspace is outside allowed roots")
}

func TestWorkspaceRootsAndBrowseExposeAllowedServerDirectories(t *testing.T) {
	ctx := context.Background()
	root := canonicalPath(t, t.TempDir())
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatalf("create project directory: %v", err)
	}
	_, _, _, handler := newIntegrationAPIWithWorkdir(t, ctx, root, fake.New())

	rootsRec := get(handler, "/api/workspaces/roots")
	if rootsRec.Code != http.StatusOK {
		t.Fatalf("expected roots status %d, got %d with body %s", http.StatusOK, rootsRec.Code, rootsRec.Body.String())
	}
	var rootsResponse workspaceRootsResponse
	decodeJSON(t, rootsRec, &rootsResponse)
	if len(rootsResponse.Roots) != 1 || rootsResponse.Roots[0].Path != root || !rootsResponse.Roots[0].Default {
		t.Fatalf("expected default root %q, got %#v", root, rootsResponse.Roots)
	}

	browseRec := get(handler, "/api/workspaces/browse?root_id="+rootsResponse.Roots[0].ID)
	if browseRec.Code != http.StatusOK {
		t.Fatalf("expected browse status %d, got %d with body %s", http.StatusOK, browseRec.Code, browseRec.Body.String())
	}
	var browseResponse workspaceBrowseResponse
	decodeJSON(t, browseRec, &browseResponse)
	if len(browseResponse.Entries) != 1 || browseResponse.Entries[0].Name != "project" || browseResponse.Entries[0].Type != "directory" {
		t.Fatalf("expected project directory, got %#v", browseResponse.Entries)
	}
}

func TestSessionFileAPIsListSearchAndReadWorkspaceFiles(t *testing.T) {
	ctx := context.Background()
	workspace := canonicalPath(t, t.TempDir())
	if err := os.Mkdir(filepath.Join(workspace, "src"), 0o755); err != nil {
		t.Fatalf("create src directory: %v", err)
	}
	source := "package main\n\nfunc main() {\n\tprintln(\"needle\")\n}\n"
	if err := os.WriteFile(filepath.Join(workspace, "src", "main.go"), []byte(source), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	dbStore, _, _, handler := newIntegrationAPIWithWorkdir(t, ctx, workspace, fake.New())
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:         "Files",
		AgentType:     "fake",
		WorkspacePath: workspace,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	listRec := get(handler, "/api/sessions/"+session.ID+"/files")
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d with body %s", http.StatusOK, listRec.Code, listRec.Body.String())
	}
	var listResponse workspaceBrowseResponse
	decodeJSON(t, listRec, &listResponse)
	if len(listResponse.Entries) != 1 || listResponse.Entries[0].Name != "src" {
		t.Fatalf("expected src entry, got %#v", listResponse.Entries)
	}

	searchRec := get(handler, "/api/sessions/"+session.ID+"/files/search?q=main")
	if searchRec.Code != http.StatusOK {
		t.Fatalf("expected search status %d, got %d with body %s", http.StatusOK, searchRec.Code, searchRec.Body.String())
	}
	var searchResponse workspaceSearchResponse
	decodeJSON(t, searchRec, &searchResponse)
	if len(searchResponse.Results) != 1 || searchResponse.Results[0].Path != "src/main.go" {
		t.Fatalf("expected src/main.go search result, got %#v", searchResponse.Results)
	}
	if searchResponse.Results[0].MatchType != "name" {
		t.Fatalf("expected name search result, got %#v", searchResponse.Results[0])
	}

	contentSearchRec := get(handler, "/api/sessions/"+session.ID+"/files/search?q=needle")
	if contentSearchRec.Code != http.StatusOK {
		t.Fatalf("expected content search status %d, got %d with body %s", http.StatusOK, contentSearchRec.Code, contentSearchRec.Body.String())
	}
	var contentSearchResponse workspaceSearchResponse
	decodeJSON(t, contentSearchRec, &contentSearchResponse)
	if len(contentSearchResponse.Results) != 1 || contentSearchResponse.Results[0].Path != "src/main.go" {
		t.Fatalf("expected src/main.go content search result, got %#v", contentSearchResponse.Results)
	}
	if contentSearchResponse.Results[0].MatchType != "content" || contentSearchResponse.Results[0].LineNumber != 4 || !strings.Contains(contentSearchResponse.Results[0].LineText, "needle") {
		t.Fatalf("expected content search metadata, got %#v", contentSearchResponse.Results[0])
	}

	contentRec := get(handler, "/api/sessions/"+session.ID+"/files/content?path="+url.QueryEscape("src/main.go"))
	if contentRec.Code != http.StatusOK {
		t.Fatalf("expected content status %d, got %d with body %s", http.StatusOK, contentRec.Code, contentRec.Body.String())
	}
	var contentResponse workspaceFileContentResponse
	decodeJSON(t, contentRec, &contentResponse)
	if contentResponse.Content != source || contentResponse.Encoding != "utf-8" {
		t.Fatalf("expected text file content, got %#v", contentResponse)
	}

	updateRec := putJSON(handler, "/api/sessions/"+session.ID+"/files/content?path="+url.QueryEscape("src/main.go"), `{"content":"package main\n\nfunc main() {}\n"}`)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("expected update status %d, got %d with body %s", http.StatusOK, updateRec.Code, updateRec.Body.String())
	}
	var updatedContent workspaceFileContentResponse
	decodeJSON(t, updateRec, &updatedContent)
	if updatedContent.Content != "package main\n\nfunc main() {}\n" {
		t.Fatalf("expected updated content, got %#v", updatedContent.Content)
	}
	persisted, err := os.ReadFile(filepath.Join(workspace, "src", "main.go"))
	if err != nil {
		t.Fatalf("read persisted source file: %v", err)
	}
	if string(persisted) != updatedContent.Content {
		t.Fatalf("expected persisted content %q, got %q", updatedContent.Content, string(persisted))
	}

	if err := os.WriteFile(filepath.Join(workspace, "src", "image.bin"), []byte{0x00, 0x01}, 0o644); err != nil {
		t.Fatalf("write binary file: %v", err)
	}
	binaryUpdateRec := putJSON(handler, "/api/sessions/"+session.ID+"/files/content?path="+url.QueryEscape("src/image.bin"), `{"content":"text"}`)
	if binaryUpdateRec.Code != http.StatusBadRequest {
		t.Fatalf("expected binary update status %d, got %d with body %s", http.StatusBadRequest, binaryUpdateRec.Code, binaryUpdateRec.Body.String())
	}
	assertErrorResponse(t, binaryUpdateRec, "file must be UTF-8 text")
}

func TestSearchWorkspacePrioritizesNamesWithoutSkippingTemporaryPaths(t *testing.T) {
	workspace := canonicalPath(t, t.TempDir())
	for _, directory := range []string{".tmp", "a-content", "projects"} {
		if err := os.MkdirAll(filepath.Join(workspace, directory), 0o755); err != nil {
			t.Fatalf("create %s: %v", directory, err)
		}
	}
	for index := 0; index < maxSearchResults; index++ {
		path := filepath.Join(workspace, "a-content", fmt.Sprintf("%03d.txt", index))
		if err := os.WriteFile(path, []byte("insurance appears in content\n"), 0o644); err != nil {
			t.Fatalf("write content match: %v", err)
		}
	}
	for _, path := range []string{".tmp/insurance-cache.txt", "projects/insurance-medical.md"} {
		if err := os.WriteFile(filepath.Join(workspace, path), []byte("no matching body\n"), 0o644); err != nil {
			t.Fatalf("write name match: %v", err)
		}
	}

	results, err := searchWorkspace(context.Background(), workspace, workspace, "insurance")
	if err != nil {
		t.Fatalf("search workspace: %v", err)
	}
	if len(results) != maxSearchResults {
		t.Fatalf("expected %d results, got %d", maxSearchResults, len(results))
	}
	if results[0].Path != ".tmp/insurance-cache.txt" || results[0].MatchType != "name" {
		t.Fatalf("expected temporary filename match first, got %#v", results[0])
	}
	if results[1].Path != "projects/insurance-medical.md" || results[1].MatchType != "name" {
		t.Fatalf("expected project filename match second, got %#v", results[1])
	}
}

func TestSearchWorkspaceStopsWhenContextIsCanceled(t *testing.T) {
	workspace := canonicalPath(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := searchWorkspace(ctx, workspace, workspace, "insurance")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestSessionFileRawStreamsMediaAndDownloads(t *testing.T) {
	ctx := context.Background()
	workspace := canonicalPath(t, t.TempDir())
	imageData := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x00, 0x01, 0x02, 0x03}, maxFilePreviewBytes/2)...)
	heicData := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'c'}
	files := map[string][]byte{
		"component.ts": []byte("export const answer: number = 42\n"),
		"image.png":    imageData,
		"page.html":    {0xff, 0xfe, '<', 0x00, 'h', 0x00, 't', 0x00, 'm', 0x00, 'l', 0x00, '>'},
		"photo.heic":   heicData,
		"segment.ts":   {0x47, 0xff, 0x00, 0x10},
		"notes.txt":    []byte("download me"),
		"vector.svg":   []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(workspace, name), content, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	dbStore, _, _, handler := newIntegrationAPIWithWorkdir(t, ctx, workspace, fake.New())
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:         "Raw files",
		AgentType:     "fake",
		WorkspacePath: workspace,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	contentRec := get(handler, "/api/sessions/"+session.ID+"/files/content?path=photo.heic")
	if contentRec.Code != http.StatusOK {
		t.Fatalf("expected HEIC content status %d, got %d with body %s", http.StatusOK, contentRec.Code, contentRec.Body.String())
	}
	var content workspaceFileContentResponse
	decodeJSON(t, contentRec, &content)
	if content.Encoding != "binary" || content.MediaType != "image/heic" || content.PreviewKind != "image" {
		t.Fatalf("expected HEIC image metadata, got %#v", content)
	}
	typeScriptRec := get(handler, "/api/sessions/"+session.ID+"/files/content?path=component.ts")
	if typeScriptRec.Code != http.StatusOK {
		t.Fatalf("expected TypeScript content status %d, got %d with body %s", http.StatusOK, typeScriptRec.Code, typeScriptRec.Body.String())
	}
	var typeScriptContent workspaceFileContentResponse
	decodeJSON(t, typeScriptRec, &typeScriptContent)
	if typeScriptContent.Encoding != "utf-8" || typeScriptContent.MediaType != "text/typescript" || typeScriptContent.PreviewKind != "none" {
		t.Fatalf("expected TypeScript text metadata, got %#v", typeScriptContent)
	}
	transportStreamRec := get(handler, "/api/sessions/"+session.ID+"/files/content?path=segment.ts")
	if transportStreamRec.Code != http.StatusOK {
		t.Fatalf("expected transport stream content status %d, got %d with body %s", http.StatusOK, transportStreamRec.Code, transportStreamRec.Body.String())
	}
	var transportStreamContent workspaceFileContentResponse
	decodeJSON(t, transportStreamRec, &transportStreamContent)
	if transportStreamContent.Encoding != "binary" || transportStreamContent.MediaType != "video/mp2t" || transportStreamContent.PreviewKind != "video" {
		t.Fatalf("expected MPEG transport stream metadata, got %#v", transportStreamContent)
	}

	imageRec := get(handler, "/api/sessions/"+session.ID+"/files/raw?path=image.png")
	if imageRec.Code != http.StatusOK {
		t.Fatalf("expected image status %d, got %d with body %s", http.StatusOK, imageRec.Code, imageRec.Body.String())
	}
	if !bytes.Equal(imageRec.Body.Bytes(), imageData) {
		t.Fatalf("expected full image response of %d bytes, got %d", len(imageData), imageRec.Body.Len())
	}
	if got := imageRec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("expected image/png content type, got %q", got)
	}
	if got := imageRec.Header().Get("Content-Disposition"); got != `inline; filename=image.png` {
		t.Fatalf("expected inline image disposition, got %q", got)
	}
	if got := imageRec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("expected nosniff header, got %q", got)
	}

	rangeReq := httptest.NewRequest(http.MethodGet, "/api/sessions/"+session.ID+"/files/raw?path=image.png", nil)
	rangeReq.Header.Set("Range", "bytes=8-11")
	rangeRec := httptest.NewRecorder()
	handler.ServeHTTP(rangeRec, rangeReq)
	if rangeRec.Code != http.StatusPartialContent || !bytes.Equal(rangeRec.Body.Bytes(), imageData[8:12]) {
		t.Fatalf("expected byte range %v, got status %d and %v", imageData[8:12], rangeRec.Code, rangeRec.Body.Bytes())
	}
	if got := rangeRec.Header().Get("Content-Range"); got != fmt.Sprintf("bytes 8-11/%d", len(imageData)) {
		t.Fatalf("unexpected content range %q", got)
	}

	downloadRec := get(handler, "/api/sessions/"+session.ID+"/files/raw?path=notes.txt&download=1")
	if downloadRec.Code != http.StatusOK || downloadRec.Body.String() != "download me" {
		t.Fatalf("expected text download, got status %d and body %q", downloadRec.Code, downloadRec.Body.String())
	}
	if got := downloadRec.Header().Get("Content-Disposition"); got != `attachment; filename=notes.txt` {
		t.Fatalf("expected attachment disposition, got %q", got)
	}
	rawTextRec := get(handler, "/api/sessions/"+session.ID+"/files/raw?path=notes.txt&raw=1")
	if rawTextRec.Code != http.StatusOK || rawTextRec.Body.String() != "download me" {
		t.Fatalf("expected raw text response, got status %d and body %q", rawTextRec.Code, rawTextRec.Body.String())
	}
	if got := rawTextRec.Header().Get("Content-Disposition"); got != `inline; filename=notes.txt` {
		t.Fatalf("expected inline raw text disposition, got %q", got)
	}
	if got := rawTextRec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("expected inert raw text content type, got %q", got)
	}

	svgRec := get(handler, "/api/sessions/"+session.ID+"/files/raw?path=vector.svg")
	if got := svgRec.Header().Get("Content-Disposition"); got != `attachment; filename=vector.svg` {
		t.Fatalf("expected active image content to download, got %q", got)
	}
	rawSVGRec := get(handler, "/api/sessions/"+session.ID+"/files/raw?path=vector.svg&raw=1")
	if got := rawSVGRec.Header().Get("Content-Disposition"); got != `inline; filename=vector.svg` {
		t.Fatalf("expected raw SVG to open inline, got %q", got)
	}
	if got := rawSVGRec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("expected raw SVG to use inert text content type, got %q", got)
	}
	rawHTMLRec := get(handler, "/api/sessions/"+session.ID+"/files/raw?path=page.html&raw=1")
	if got := rawHTMLRec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("expected raw binary-encoded HTML to use inert text content type, got %q", got)
	}
}

func TestCompletedAssistantFileCitationStreamsAllowedFileOutsideSessionWorkspace(t *testing.T) {
	ctx := context.Background()
	root := canonicalPath(t, t.TempDir())
	workspace := filepath.Join(root, "project")
	outputDirectory := filepath.Join(root, "Documents", "Ryann Responses")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := os.MkdirAll(outputDirectory, 0o755); err != nil {
		t.Fatalf("create output directory: %v", err)
	}
	outputPath := filepath.Join(outputDirectory, "certificate-of-completion-OPP_17893173627841.pdf")
	output := []byte("%PDF-1.7\nverified")
	if err := os.WriteFile(outputPath, output, 0o644); err != nil {
		t.Fatalf("write cited output: %v", err)
	}
	dbStore, _, _, handler := newIntegrationAPIWithWorkdir(t, ctx, root, fake.New())
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:         "Cited output",
		AgentType:     "fake",
		WorkspacePath: workspace,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"text": fmt.Sprintf(":codex-file-citation{path=%q purpose=\"output\"}", outputPath),
	})
	if err != nil {
		t.Fatalf("marshal event payload: %v", err)
	}
	event, err := dbStore.AppendEvent(ctx, store.AppendEventParams{
		SessionID: session.ID,
		Type:      "agent.message.completed",
		Role:      "assistant",
		Status:    store.EventStatusCompleted,
		Payload:   payload,
	})
	if err != nil {
		t.Fatalf("append cited output event: %v", err)
	}

	path := fmt.Sprintf("/api/sessions/%s/events/%d/file-citation?path=%s", session.ID, event.Seq, url.QueryEscape(outputPath))
	rec := get(handler, path)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), output) {
		t.Fatalf("expected cited output, got status %d and body %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/pdf" {
		t.Fatalf("expected PDF content type, got %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != `inline; filename=certificate-of-completion-OPP_17893173627841.pdf` {
		t.Fatalf("expected inline citation, got %q", got)
	}

	unreferenced := filepath.Join(root, "Documents", "unreferenced.pdf")
	if err := os.WriteFile(unreferenced, []byte("private"), 0o644); err != nil {
		t.Fatalf("write unreferenced file: %v", err)
	}
	rejected := get(handler, fmt.Sprintf("/api/sessions/%s/events/%d/file-citation?path=%s", session.ID, event.Seq, url.QueryEscape(unreferenced)))
	if rejected.Code != http.StatusNotFound {
		t.Fatalf("expected unreferenced path status %d, got %d", http.StatusNotFound, rejected.Code)
	}

	outsidePath := filepath.Join(canonicalPath(t, t.TempDir()), "cited-secret.pdf")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o644); err != nil {
		t.Fatalf("write outside cited file: %v", err)
	}
	outsidePayload, err := json.Marshal(map[string]any{
		"text": fmt.Sprintf(":codex-file-citation{path=%q purpose=\"output\"}", outsidePath),
	})
	if err != nil {
		t.Fatalf("marshal outside event payload: %v", err)
	}
	outsideEvent, err := dbStore.AppendEvent(ctx, store.AppendEventParams{
		SessionID: session.ID,
		Type:      "agent.message.completed",
		Role:      "assistant",
		Status:    store.EventStatusCompleted,
		Payload:   outsidePayload,
	})
	if err != nil {
		t.Fatalf("append outside cited output event: %v", err)
	}
	outsideRec := get(handler, fmt.Sprintf("/api/sessions/%s/events/%d/file-citation?path=%s", session.ID, outsideEvent.Seq, url.QueryEscape(outsidePath)))
	if outsideRec.Code != http.StatusForbidden {
		t.Fatalf("expected outside-root path status %d, got %d", http.StatusForbidden, outsideRec.Code)
	}
}

func TestSessionFileRawRejectsUnsafePathsAndDirectories(t *testing.T) {
	ctx := context.Background()
	workspace := canonicalPath(t, t.TempDir())
	outside := canonicalPath(t, t.TempDir())
	if err := os.Mkdir(filepath.Join(workspace, "directory"), 0o755); err != nil {
		t.Fatalf("create directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.bin"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.bin"), filepath.Join(workspace, "outside.bin")); err != nil {
		t.Fatalf("create outside symlink: %v", err)
	}
	dbStore, _, _, handler := newIntegrationAPIWithWorkdir(t, ctx, workspace, fake.New())
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:         "Raw file security",
		AgentType:     "fake",
		WorkspacePath: workspace,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	traversalRec := get(handler, "/api/sessions/"+session.ID+"/files/raw?path="+url.QueryEscape("../secret.bin"))
	if traversalRec.Code != http.StatusBadRequest {
		t.Fatalf("expected traversal status %d, got %d", http.StatusBadRequest, traversalRec.Code)
	}
	directoryRec := get(handler, "/api/sessions/"+session.ID+"/files/raw?path=directory")
	if directoryRec.Code != http.StatusBadRequest {
		t.Fatalf("expected directory status %d, got %d", http.StatusBadRequest, directoryRec.Code)
	}
	symlinkRec := get(handler, "/api/sessions/"+session.ID+"/files/raw?path=outside.bin")
	if symlinkRec.Code != http.StatusForbidden {
		t.Fatalf("expected outside symlink status %d, got %d", http.StatusForbidden, symlinkRec.Code)
	}
}

func TestSessionFileUploadWritesFilesToSelectedDirectory(t *testing.T) {
	ctx := context.Background()
	workspace := canonicalPath(t, t.TempDir())
	if err := os.Mkdir(filepath.Join(workspace, "src"), 0o755); err != nil {
		t.Fatalf("create src directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "src", "existing.txt"), []byte("old"), 0o600); err != nil {
		t.Fatalf("write existing file: %v", err)
	}
	dbStore, _, _, handler := newIntegrationAPIWithWorkdir(t, ctx, workspace, fake.New())
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:         "Uploads",
		AgentType:     "fake",
		WorkspacePath: workspace,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, content := range map[string]string{"new.txt": "new file", "existing.txt": "replacement"} {
		part, err := writer.CreateFormFile("files", name)
		if err != nil {
			t.Fatalf("create multipart file %q: %v", name, err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatalf("write multipart file %q: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/files/upload?path=src", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected upload status %d, got %d with body %s", http.StatusCreated, rec.Code, rec.Body.String())
	}
	var response workspaceFileUploadResponse
	decodeJSON(t, rec, &response)
	if len(response.Files) != 2 {
		t.Fatalf("expected two uploaded files, got %#v", response.Files)
	}
	for name, expected := range map[string]string{"new.txt": "new file", "existing.txt": "replacement"} {
		content, err := os.ReadFile(filepath.Join(workspace, "src", name))
		if err != nil {
			t.Fatalf("read uploaded file %q: %v", name, err)
		}
		if string(content) != expected {
			t.Fatalf("expected uploaded file %q content %q, got %q", name, expected, string(content))
		}
	}
	info, err := os.Stat(filepath.Join(workspace, "src", "existing.txt"))
	if err != nil {
		t.Fatalf("stat replaced file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected replaced file permissions 0600, got %o", info.Mode().Perm())
	}
}

func TestSessionConsoleStatusStartAndKill(t *testing.T) {
	ctx := context.Background()
	workspace := canonicalPath(t, t.TempDir())
	dbStore, _, _, handler := newIntegrationAPIWithWorkdir(t, ctx, workspace, fake.New())
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:         "Console",
		AgentType:     "fake",
		WorkspacePath: workspace,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	statusRec := get(handler, "/api/sessions/"+session.ID+"/console")
	if statusRec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, statusRec.Code, statusRec.Body.String())
	}
	var initialStatus struct {
		Running       bool   `json:"running"`
		WorkspacePath string `json:"workspace_path"`
	}
	decodeJSON(t, statusRec, &initialStatus)
	if initialStatus.Running {
		t.Fatalf("expected console to be stopped initially, got %#v", initialStatus)
	}
	if initialStatus.WorkspacePath != workspace {
		t.Fatalf("expected workspace path %q, got %q", workspace, initialStatus.WorkspacePath)
	}

	startRec := postJSON(handler, "/api/sessions/"+session.ID+"/console", `{}`)
	if startRec.Code != http.StatusOK {
		t.Fatalf("expected start status %d, got %d with body %s", http.StatusOK, startRec.Code, startRec.Body.String())
	}
	var startedStatus struct {
		Running       bool   `json:"running"`
		WorkspacePath string `json:"workspace_path"`
	}
	decodeJSON(t, startRec, &startedStatus)
	if !startedStatus.Running {
		t.Fatalf("expected console to be running, got %#v", startedStatus)
	}
	if startedStatus.WorkspacePath != workspace {
		t.Fatalf("expected workspace path %q, got %q", workspace, startedStatus.WorkspacePath)
	}

	runningRec := get(handler, "/api/sessions/"+session.ID+"/console")
	if runningRec.Code != http.StatusOK {
		t.Fatalf("expected running status %d, got %d with body %s", http.StatusOK, runningRec.Code, runningRec.Body.String())
	}
	var runningStatus struct {
		Running bool `json:"running"`
	}
	decodeJSON(t, runningRec, &runningStatus)
	if !runningStatus.Running {
		t.Fatalf("expected console to remain running after start response")
	}

	killReq := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+session.ID+"/console", nil)
	killRec := httptest.NewRecorder()
	handler.ServeHTTP(killRec, killReq)
	if killRec.Code != http.StatusNoContent {
		t.Fatalf("expected kill status %d, got %d with body %s", http.StatusNoContent, killRec.Code, killRec.Body.String())
	}
}

func TestSessionFilesIncludesGitSummary(t *testing.T) {
	ctx := context.Background()
	workspace := canonicalPath(t, t.TempDir())
	runGit(t, workspace, "init")
	runGit(t, workspace, "checkout", "-b", "feature/files-pill")
	if err := os.WriteFile(filepath.Join(workspace, "modified.txt"), []byte("original\n"), 0o644); err != nil {
		t.Fatalf("write modified seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "deleted.txt"), []byte("delete me\n"), 0o644); err != nil {
		t.Fatalf("write deleted seed: %v", err)
	}
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "-c", "user.name=Gorchestra Test", "-c", "user.email=test@example.com", "commit", "-m", "seed")

	if err := os.WriteFile(filepath.Join(workspace, "modified.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}
	if err := os.Remove(filepath.Join(workspace, "deleted.txt")); err != nil {
		t.Fatalf("delete tracked file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "added.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write added file: %v", err)
	}

	dbStore, _, _, handler := newIntegrationAPIWithWorkdir(t, ctx, workspace, fake.New())
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:         "Git files",
		AgentType:     "fake",
		WorkspacePath: workspace,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	listRec := get(handler, "/api/sessions/"+session.ID+"/files")
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d with body %s", http.StatusOK, listRec.Code, listRec.Body.String())
	}
	var response workspaceBrowseResponse
	decodeJSON(t, listRec, &response)
	if response.GitSummary == nil {
		t.Fatal("expected git summary")
	}
	if response.GitSummary.Branch != "feature/files-pill" {
		t.Fatalf("expected branch feature/files-pill, got %q", response.GitSummary.Branch)
	}
	if response.GitSummary.Added != 1 || response.GitSummary.Modified != 1 || response.GitSummary.Deleted != 1 {
		t.Fatalf("expected added/modified/deleted counts of 1, got %#v", response.GitSummary)
	}
}

func TestSessionFilesIncludesCleanGitBranch(t *testing.T) {
	ctx := context.Background()
	workspace := canonicalPath(t, t.TempDir())
	runGit(t, workspace, "init")
	runGit(t, workspace, "checkout", "-b", "clean-branch")
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# Clean\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "-c", "user.name=Gorchestra Test", "-c", "user.email=test@example.com", "commit", "-m", "seed")

	dbStore, _, _, handler := newIntegrationAPIWithWorkdir(t, ctx, workspace, fake.New())
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:         "Clean git files",
		AgentType:     "fake",
		WorkspacePath: workspace,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	listRec := get(handler, "/api/sessions/"+session.ID+"/files")
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d with body %s", http.StatusOK, listRec.Code, listRec.Body.String())
	}
	var response workspaceBrowseResponse
	decodeJSON(t, listRec, &response)
	if response.GitSummary == nil {
		t.Fatal("expected git summary")
	}
	if response.GitSummary.Branch != "clean-branch" {
		t.Fatalf("expected branch clean-branch, got %q", response.GitSummary.Branch)
	}
	if response.GitSummary.Added != 0 || response.GitSummary.Modified != 0 || response.GitSummary.Deleted != 0 {
		t.Fatalf("expected zero file counts, got %#v", response.GitSummary)
	}
}

func TestCreateSessionRejectsUnsupportedAgent(t *testing.T) {
	ctx := context.Background()
	_, _, _, handler := newIntegrationAPI(t, ctx, fake.New())

	rec := postJSON(handler, "/api/sessions", `{"agent_type":"codex"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "unsupported agent_type")
}

func TestCreateSessionAcceptsAvailableCodexAgent(t *testing.T) {
	ctx := context.Background()
	codexAgent := availabilityAgent{agentType: "codex"}
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, codexAgent)

	rec := postJSON(handler, "/api/sessions", `{"agent_type":"codex","title":"Real run"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusCreated, rec.Code, rec.Body.String())
	}

	var response createSessionResponse
	decodeJSON(t, rec, &response)
	session, err := dbStore.GetSession(ctx, response.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.AgentType != "codex" {
		t.Fatalf("expected codex agent type, got %q", session.AgentType)
	}
	if decodeTestAgentOptions(t, session.AgentOptions)["codex"]["permission_policy"] != "ask" {
		t.Fatalf("expected new codex session to ask for permissions, got %s", session.AgentOptions)
	}
}

func TestCreateSessionAcceptsAvailableClaudeAgent(t *testing.T) {
	ctx := context.Background()
	claudeAgent := availabilityAgent{agentType: "claude"}
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, claudeAgent)

	rec := postJSON(handler, "/api/sessions", `{"agent_type":"claude","title":"Claude run"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusCreated, rec.Code, rec.Body.String())
	}

	var response createSessionResponse
	decodeJSON(t, rec, &response)
	session, err := dbStore.GetSession(ctx, response.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.AgentType != "claude" {
		t.Fatalf("expected claude agent type, got %q", session.AgentType)
	}
	if decodeTestAgentOptions(t, session.AgentOptions)["claude"]["permission_policy"] != "ask" {
		t.Fatalf("expected new claude session to ask for permissions, got %s", session.AgentOptions)
	}
}

func TestCreateSessionAcceptsAvailableOpenCodeAgent(t *testing.T) {
	ctx := context.Background()
	opencodeAgent := availabilityAgent{agentType: "opencode"}
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, opencodeAgent)

	rec := postJSON(handler, "/api/sessions", `{"agent_type":"opencode","title":"OpenCode run"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusCreated, rec.Code, rec.Body.String())
	}

	var response createSessionResponse
	decodeJSON(t, rec, &response)
	session, err := dbStore.GetSession(ctx, response.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.AgentType != "opencode" {
		t.Fatalf("expected opencode agent type, got %q", session.AgentType)
	}
	if decodeTestAgentOptions(t, session.AgentOptions)["opencode"]["permission_policy"] != "ask" {
		t.Fatalf("expected new opencode session to ask for permissions, got %s", session.AgentOptions)
	}
}

func TestCreateSessionAcceptsAvailablePiAgent(t *testing.T) {
	ctx := context.Background()
	piAgent := availabilityAgent{agentType: "pi"}
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, piAgent)

	rec := postJSON(handler, "/api/sessions", `{"agent_type":"pi","title":"Pi run"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusCreated, rec.Code, rec.Body.String())
	}

	var response createSessionResponse
	decodeJSON(t, rec, &response)
	session, err := dbStore.GetSession(ctx, response.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.AgentType != "pi" {
		t.Fatalf("expected pi agent type, got %q", session.AgentType)
	}
}

func TestCreateSessionStoresCodexRunDangerouslyOption(t *testing.T) {
	ctx := context.Background()
	codexAgent := availabilityAgent{agentType: "codex"}
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, codexAgent)

	rec := postJSON(handler, "/api/sessions", `{
		"agent_type":"codex",
		"title":"Danger run",
		"agent_options":{"codex":{"run_dangerously":true}}
	}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusCreated, rec.Code, rec.Body.String())
	}

	var response createSessionResponse
	decodeJSON(t, rec, &response)
	session, err := dbStore.GetSession(ctx, response.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	var options map[string]map[string]any
	if err := json.Unmarshal(session.AgentOptions, &options); err != nil {
		t.Fatalf("decode agent options: %v", err)
	}
	if options["codex"]["run_dangerously"] != true {
		t.Fatalf("expected run_dangerously option, got %#v", options)
	}

	var sessionResponse sessionResponse
	getRec := get(handler, "/api/sessions/"+response.SessionID)
	decodeJSON(t, getRec, &sessionResponse)
	responseOptions, ok := sessionResponse.AgentOptions.(map[string]any)
	if !ok {
		t.Fatalf("expected response agent options map, got %#v", sessionResponse.AgentOptions)
	}
	codexOptions, ok := responseOptions["codex"].(map[string]any)
	if !ok || codexOptions["run_dangerously"] != true {
		t.Fatalf("expected response run_dangerously option, got %#v", responseOptions)
	}
}

func TestCreateSessionStoresClaudeRunDangerouslyOption(t *testing.T) {
	ctx := context.Background()
	claudeAgent := availabilityAgent{agentType: "claude"}
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, claudeAgent)

	rec := postJSON(handler, "/api/sessions", `{
		"agent_type":"claude",
		"title":"Danger run",
		"agent_options":{"claude":{"run_dangerously":true}}
	}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusCreated, rec.Code, rec.Body.String())
	}

	var response createSessionResponse
	decodeJSON(t, rec, &response)
	session, err := dbStore.GetSession(ctx, response.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	var options map[string]map[string]any
	if err := json.Unmarshal(session.AgentOptions, &options); err != nil {
		t.Fatalf("decode agent options: %v", err)
	}
	if options["claude"]["run_dangerously"] != true {
		t.Fatalf("expected run_dangerously option, got %#v", options)
	}

	var sessionResponse sessionResponse
	getRec := get(handler, "/api/sessions/"+response.SessionID)
	decodeJSON(t, getRec, &sessionResponse)
	responseOptions, ok := sessionResponse.AgentOptions.(map[string]any)
	if !ok {
		t.Fatalf("expected response agent options map, got %#v", sessionResponse.AgentOptions)
	}
	claudeOptions, ok := responseOptions["claude"].(map[string]any)
	if !ok || claudeOptions["run_dangerously"] != true {
		t.Fatalf("expected response run_dangerously option, got %#v", responseOptions)
	}
}

func TestListSessionsExposesPendingInputActivity(t *testing.T) {
	ctx := context.Background()
	dbStore, events, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:     "Needs input",
		AgentType: "fake",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := dbStore.UpdateSessionStatus(ctx, store.UpdateSessionStatusParams{
		ID:     session.ID,
		Status: store.SessionStatusRunning,
	}); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if _, err := events.Append(ctx, eventservice.AppendParams{
		SessionID: session.ID,
		Type:      "agent.input.requested",
		Role:      "assistant",
		Status:    store.EventStatusStarted,
		Payload:   json.RawMessage(`{"request_id":"call_test","questions":[{"id":"question_test","question":"Pick one"}]}`),
	}); err != nil {
		t.Fatalf("append input requested: %v", err)
	}

	rec := get(handler, "/api/sessions")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response listSessionsResponse
	decodeJSON(t, rec, &response)
	if len(response.Sessions) != 1 {
		t.Fatalf("expected one session, got %#v", response.Sessions)
	}
	if !response.Sessions[0].PendingInput {
		t.Fatalf("expected pending_input true, got %#v", response.Sessions[0])
	}
	if response.Sessions[0].LastEventSeq != 1 {
		t.Fatalf("expected last_event_seq 1, got %d", response.Sessions[0].LastEventSeq)
	}

	if _, err := events.Append(ctx, eventservice.AppendParams{
		SessionID: session.ID,
		Type:      "agent.input.answered",
		Role:      "user",
		Status:    store.EventStatusCompleted,
		Payload:   json.RawMessage(`{"request_id":"call_test"}`),
	}); err != nil {
		t.Fatalf("append input answered: %v", err)
	}

	rec = get(handler, "/api/sessions")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	decodeJSON(t, rec, &response)
	if response.Sessions[0].PendingInput {
		t.Fatalf("expected pending_input false after answer, got %#v", response.Sessions[0])
	}
	if response.Sessions[0].LastEventSeq != 2 {
		t.Fatalf("expected last_event_seq 2, got %d", response.Sessions[0].LastEventSeq)
	}
}

func TestCreateSessionReturnsUnavailableForRegisteredUnavailableAgent(t *testing.T) {
	ctx := context.Background()
	codexAgent := availabilityAgent{agentType: "codex", availableErr: agents.ErrUnavailable}
	_, _, _, handler := newIntegrationAPI(t, ctx, codexAgent)

	rec := postJSON(handler, "/api/sessions", `{"agent_type":"codex"}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "agent unavailable")
}

func TestAgentOptionsReturnsProviderOptions(t *testing.T) {
	ctx := context.Background()
	codexAgent := optionsAgent{
		agentType: "codex",
		options: agents.Options{
			DefaultModel: "gpt-5.5",
			Models: []agents.ModelOption{
				{
					Model:                  "gpt-5.5",
					DisplayName:            "GPT-5.5",
					DefaultReasoningEffort: "medium",
					IsDefault:              true,
				},
			},
			CollaborationModes: []agents.CollaborationModeOption{{Name: "Plan", Mode: "plan"}},
		},
	}
	_, _, _, handler := newIntegrationAPI(t, ctx, codexAgent)

	req := httptest.NewRequest(http.MethodGet, "/api/agents/codex/options", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response agents.Options
	decodeJSON(t, rec, &response)
	if response.DefaultModel != "gpt-5.5" {
		t.Fatalf("expected default model gpt-5.5, got %q", response.DefaultModel)
	}
	if len(response.Models) != 1 || response.Models[0].DisplayName != "GPT-5.5" {
		t.Fatalf("expected model options, got %#v", response.Models)
	}
	if len(response.CollaborationModes) != 1 || response.CollaborationModes[0].Mode != "plan" {
		t.Fatalf("expected plan collaboration mode, got %#v", response.CollaborationModes)
	}
}

func TestSessionSkillsReturnsEnabledCatalogForWorkspace(t *testing.T) {
	ctx := context.Background()
	workdir := canonicalPath(t, t.TempDir())
	agent := newSkillBlockingAgent(agents.SkillCatalog{
		Skills: []agents.Skill{
			{
				Name:             "openai-docs",
				Description:      "Use official OpenAI documentation.",
				DisplayName:      "OpenAI Docs",
				ShortDescription: "Official product guidance",
				BrandColor:       "#10A37F",
				Path:             "/skills/user/openai-docs/SKILL.md",
				Scope:            "user",
				Enabled:          true,
			},
			{
				Name:        "disabled-skill",
				Description: "Disabled",
				Path:        "/skills/disabled/SKILL.md",
				Scope:       "system",
				Enabled:     false,
			},
		},
		Errors: []agents.SkillError{{Path: "/broken/SKILL.md", Message: "invalid frontmatter"}},
	})
	dbStore, _, _, handler := newIntegrationAPIWithWorkdir(t, ctx, workdir, agent)
	session := createIntegrationSession(t, ctx, dbStore)
	t.Cleanup(agent.release)

	rec := get(handler, "/api/sessions/"+session.ID+"/skills?refresh=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response agents.SkillCatalog
	decodeJSON(t, rec, &response)
	if len(response.Skills) != 1 || response.Skills[0].Name != "openai-docs" || response.Skills[0].DisplayName != "OpenAI Docs" {
		t.Fatalf("expected enabled skill metadata, got %#v", response.Skills)
	}
	if len(response.Errors) != 1 || response.Errors[0].Path != "/broken/SKILL.md" {
		t.Fatalf("expected discovery errors, got %#v", response.Errors)
	}
	select {
	case query := <-agent.queries:
		if query.Workdir != workdir || !query.ForceReload {
			t.Fatalf("expected refreshed query for %q, got %#v", workdir, query)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for skill query")
	}
}

func TestMessageSubmissionValidatesAndPassesStructuredSkills(t *testing.T) {
	ctx := context.Background()
	workdir := canonicalPath(t, t.TempDir())
	selected := agents.SkillReference{Name: "openai-docs", Path: "/skills/user/openai-docs/SKILL.md"}
	agent := newSkillBlockingAgent(agents.SkillCatalog{Skills: []agents.Skill{
		{Name: selected.Name, Path: selected.Path, Scope: "user", Enabled: true},
	}})
	dbStore, _, _, handler := newIntegrationAPIWithWorkdir(t, ctx, workdir, agent)
	session := createIntegrationSession(t, ctx, dbStore)
	t.Cleanup(agent.release)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{
		"skills":[{"name":"openai-docs","path":"/skills/user/openai-docs/SKILL.md"}]
	}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	select {
	case input := <-agent.started:
		if input.Message != "" || !reflect.DeepEqual(input.Skills, []agents.SkillReference{selected}) {
			t.Fatalf("expected skill-only structured input, got %#v", input)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent input")
	}

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	if len(events) == 0 || events[0].Type != "user.message.completed" {
		t.Fatalf("expected persisted user message first, got %#v", events)
	}
	payload := decodeEventPayload(t, events[0])
	skills, ok := payload["skills"].([]any)
	if !ok || len(skills) != 1 {
		t.Fatalf("expected persisted skills payload, got %#v", payload["skills"])
	}

	agent.release()
	waitFor(t, func() bool {
		loaded, err := dbStore.GetSession(ctx, session.ID)
		return err == nil && loaded.Status == store.SessionStatusIdle
	})
}

func TestMessageSubmissionRejectsUnadvertisedSkillPath(t *testing.T) {
	ctx := context.Background()
	agent := newSkillBlockingAgent(agents.SkillCatalog{Skills: []agents.Skill{
		{Name: "openai-docs", Path: "/skills/user/openai-docs/SKILL.md", Enabled: true},
	}})
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, agent)
	session := createIntegrationSession(t, ctx, dbStore)
	t.Cleanup(agent.release)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{
		"content":"Use the docs",
		"skills":[{"name":"openai-docs","path":"/tmp/forged/SKILL.md"}]
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, `skill "openai-docs" is not available to this session`)

	if events := listIntegrationEvents(t, ctx, dbStore, session.ID); len(events) != 0 {
		t.Fatalf("expected rejected skill to persist no events, got %#v", events)
	}
}

func TestCreateSessionRejectsMissingAgentType(t *testing.T) {
	ctx := context.Background()
	_, _, _, handler := newIntegrationAPI(t, ctx, fake.New())

	rec := postJSON(handler, "/api/sessions", `{"title":"No agent"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "agent_type is required")
}

func TestMessageSubmissionPersistsUserMessageAndMarksSessionRunning(t *testing.T) {
	ctx := context.Background()
	agent := newBlockingAgent()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, agent)
	session := createIntegrationSession(t, ctx, dbStore)
	t.Cleanup(func() {
		agent.release()
		waitFor(t, func() bool {
			session, err := dbStore.GetSession(ctx, session.ID)
			return err == nil && session.Status == store.SessionStatusIdle
		})
	})

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{
		"content":"Inspect this repo",
		"client_submission_id":"client-submit-1"
	}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	var response submitMessageResponse
	decodeJSON(t, rec, &response)
	if response.SessionID != session.ID {
		t.Fatalf("expected session_id %q, got %q", session.ID, response.SessionID)
	}
	if response.Status != string(store.SessionStatusRunning) {
		t.Fatalf("expected response status running, got %q", response.Status)
	}

	updatedSession, err := dbStore.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if updatedSession.Status != store.SessionStatusRunning {
		t.Fatalf("expected session status running, got %q", updatedSession.Status)
	}

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	assertEventTypes(t, events, []string{"user.message.completed", "session.status.updated"})
	if events[0].Role != "user" {
		t.Fatalf("expected user role, got %q", events[0].Role)
	}
	if events[0].Status != store.EventStatusCompleted {
		t.Fatalf("expected completed user event, got %q", events[0].Status)
	}
	assertPayloadText(t, events[0], "Inspect this repo")
	payload := decodeEventPayload(t, events[0])
	if payload["client_submission_id"] != "client-submit-1" {
		t.Fatalf("expected client submission ID in user event payload, got %#v", payload)
	}
	assertPayloadStatus(t, events[1], store.SessionStatusRunning)
}

func TestMessageSubmissionAcceptsImageAttachments(t *testing.T) {
	ctx := context.Background()
	agent := newBlockingAgent()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, agent)
	session := createIntegrationSession(t, ctx, dbStore)
	t.Cleanup(func() {
		agent.release()
		waitFor(t, func() bool {
			session, err := dbStore.GetSession(ctx, session.ID)
			return err == nil && session.Status == store.SessionStatusIdle
		})
	})

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{
		"content":"Describe this image",
		"attachments":[
			{
				"name":"diagram.png",
				"media_type":"image/png",
				"data_url":"data:image/png;base64,aGVsbG8=",
				"size_bytes":5
			}
		]
	}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	var input agents.AgentInput
	select {
	case input = <-agent.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent input")
	}
	if len(input.Attachments) != 1 {
		t.Fatalf("expected one attachment, got %#v", input.Attachments)
	}
	if input.Attachments[0].Name != "diagram.png" || input.Attachments[0].DataURL != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("unexpected attachment %#v", input.Attachments[0])
	}

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	assertEventTypes(t, events, []string{"user.message.completed", "session.status.updated"})
	payload := decodeEventPayload(t, events[0])
	attachments, ok := payload["attachments"].([]any)
	if !ok || len(attachments) != 1 {
		t.Fatalf("expected attachment payload, got %#v", payload["attachments"])
	}
	attachment, ok := attachments[0].(map[string]any)
	if !ok {
		t.Fatalf("expected attachment object, got %#v", attachments[0])
	}
	if attachment["name"] != "diagram.png" || attachment["media_type"] != "image/png" {
		t.Fatalf("unexpected attachment payload %#v", attachment)
	}
}

func TestMessageSubmissionRejectsNonImageAttachments(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session := createIntegrationSession(t, ctx, dbStore)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{
		"content":"Read this",
		"attachments":[
			{
				"name":"notes.txt",
				"media_type":"text/plain",
				"data_url":"data:text/plain;base64,aGVsbG8=",
				"size_bytes":5
			}
		]
	}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "attachment 1 must be an image")
}

func TestUpdateSessionTitleTrimsAndReturnsSession(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session := createIntegrationSession(t, ctx, dbStore)

	rec := patchJSON(handler, "/api/sessions/"+session.ID, `{"title":"  New title  "}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response sessionResponse
	decodeJSON(t, rec, &response)
	if response.Title != "New title" {
		t.Fatalf("expected trimmed title, got %q", response.Title)
	}

	updated, err := dbStore.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if updated.Title != "New title" {
		t.Fatalf("expected persisted title, got %q", updated.Title)
	}
}

func TestUpdateSessionParentAttachesAndDetachesExistingSession(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	parent := createIntegrationSession(t, ctx, dbStore)
	moved, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title: "Existing", AgentType: "fake", WorkspacePath: "/existing",
	})
	if err != nil {
		t.Fatalf("create moved session: %v", err)
	}

	attach := patchJSON(handler, "/api/sessions/"+moved.ID, `{"parent_session_id":`+quoteJSON(parent.ID)+`}`)
	if attach.Code != http.StatusOK {
		t.Fatalf("expected attach status %d, got %d with body %s", http.StatusOK, attach.Code, attach.Body.String())
	}
	var response sessionResponse
	decodeJSON(t, attach, &response)
	if response.ParentSessionID != parent.ID || response.LineageDepth != 1 || response.WorkspacePath != "/existing" {
		t.Fatalf("unexpected attached session %#v", response)
	}

	detach := patchJSON(handler, "/api/sessions/"+moved.ID, `{"parent_session_id":""}`)
	if detach.Code != http.StatusOK {
		t.Fatalf("expected detach status %d, got %d with body %s", http.StatusOK, detach.Code, detach.Body.String())
	}
	var detachedResponse sessionResponse
	decodeJSON(t, detach, &detachedResponse)
	if detachedResponse.ParentSessionID != "" || detachedResponse.LineageDepth != 0 {
		t.Fatalf("unexpected detached session %#v", detachedResponse)
	}
}

func TestUpdateSessionWorkspacePreservesProviderContextAndAppendsMarker(t *testing.T) {
	ctx := context.Background()
	root := canonicalPath(t, t.TempDir())
	oldWorkspace := filepath.Join(root, "old")
	newWorkspace := filepath.Join(root, "new")
	for _, path := range []string{oldWorkspace, newWorkspace} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatalf("create workspace %s: %v", path, err)
		}
	}
	dbStore, _, _, handler := newIntegrationAPIWithWorkspaceRoots(t, ctx, root, nil, fake.New())
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:         "Workspace",
		AgentType:     "fake",
		WorkspacePath: oldWorkspace,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	session, err = dbStore.SetSessionProviderSessionID(ctx, store.SetSessionProviderSessionIDParams{
		ID:                session.ID,
		ProviderSessionID: "provider_existing",
	})
	if err != nil {
		t.Fatalf("set provider session id: %v", err)
	}

	rec := patchJSON(handler, "/api/sessions/"+session.ID, `{"workspace_path":`+quoteJSON(newWorkspace)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response sessionResponse
	decodeJSON(t, rec, &response)
	if response.WorkspacePath != newWorkspace || response.ProviderSessionID != "provider_existing" {
		t.Fatalf("unexpected updated session %#v", response)
	}
	if response.EventCount != 1 || response.LastEventSeq != 1 {
		t.Fatalf("expected marker in session counts, got %#v", response)
	}

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	assertEventTypes(t, events, []string{"session.action.completed"})
	var payload map[string]any
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("decode workspace marker payload: %v", err)
	}
	if payload["action"] != "workspace_changed" || payload["previous_workspace_path"] != oldWorkspace || payload["workspace_path"] != newWorkspace {
		t.Fatalf("unexpected workspace marker payload %#v", payload)
	}
}

func TestUpdateSessionWorkspaceRejectsInvalidAndActiveChanges(t *testing.T) {
	ctx := context.Background()
	root := canonicalPath(t, t.TempDir())
	newWorkspace := filepath.Join(root, "new")
	if err := os.Mkdir(newWorkspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	dbStore, _, _, handler := newIntegrationAPIWithWorkspaceRoots(t, ctx, root, nil, fake.New())
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:         "Workspace",
		AgentType:     "fake",
		WorkspacePath: root,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	blankRec := patchJSON(handler, "/api/sessions/"+session.ID, `{"workspace_path":"  "}`)
	if blankRec.Code != http.StatusBadRequest {
		t.Fatalf("expected blank status %d, got %d with body %s", http.StatusBadRequest, blankRec.Code, blankRec.Body.String())
	}

	outside := canonicalPath(t, t.TempDir())
	outsideRec := patchJSON(handler, "/api/sessions/"+session.ID, `{"workspace_path":`+quoteJSON(outside)+`}`)
	if outsideRec.Code != http.StatusForbidden {
		t.Fatalf("expected outside status %d, got %d with body %s", http.StatusForbidden, outsideRec.Code, outsideRec.Body.String())
	}

	if _, err := dbStore.UpdateSessionStatus(ctx, store.UpdateSessionStatusParams{ID: session.ID, Status: store.SessionStatusRunning}); err != nil {
		t.Fatalf("mark session running: %v", err)
	}
	runningRec := patchJSON(handler, "/api/sessions/"+session.ID, `{"workspace_path":`+quoteJSON(newWorkspace)+`}`)
	if runningRec.Code != http.StatusConflict {
		t.Fatalf("expected running status %d, got %d with body %s", http.StatusConflict, runningRec.Code, runningRec.Body.String())
	}
	assertErrorResponse(t, runningRec, "stop the active agent run before changing workspace")
}

func TestUpdateSessionWorkspaceRejectsActiveConsoleAndNoOpsCurrentPath(t *testing.T) {
	ctx := context.Background()
	root := canonicalPath(t, t.TempDir())
	newWorkspace := filepath.Join(root, "new")
	if err := os.Mkdir(newWorkspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	dbStore, _, _, handler := newIntegrationAPIWithWorkspaceRoots(t, ctx, root, nil, fake.New())
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:         "Workspace",
		AgentType:     "fake",
		WorkspacePath: root,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	noOpRec := patchJSON(handler, "/api/sessions/"+session.ID, `{"workspace_path":`+quoteJSON(root)+`}`)
	if noOpRec.Code != http.StatusOK {
		t.Fatalf("expected no-op status %d, got %d with body %s", http.StatusOK, noOpRec.Code, noOpRec.Body.String())
	}
	if events := listIntegrationEvents(t, ctx, dbStore, session.ID); len(events) != 0 {
		t.Fatalf("expected no marker for unchanged workspace, got %#v", events)
	}

	startRec := postJSON(handler, "/api/sessions/"+session.ID+"/console", `{}`)
	if startRec.Code != http.StatusOK {
		t.Fatalf("start console: status %d body %s", startRec.Code, startRec.Body.String())
	}
	t.Cleanup(func() { _ = deleteRequest(handler, "/api/sessions/"+session.ID+"/console") })
	consoleRec := patchJSON(handler, "/api/sessions/"+session.ID, `{"workspace_path":`+quoteJSON(newWorkspace)+`}`)
	if consoleRec.Code != http.StatusConflict {
		t.Fatalf("expected console status %d, got %d with body %s", http.StatusConflict, consoleRec.Code, consoleRec.Body.String())
	}
	assertErrorResponse(t, consoleRec, "stop the active console before changing workspace")
}

func TestUpdateCodexSessionAgentOptions(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:     "Codex",
		AgentType: "codex",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	rec := patchJSON(handler, "/api/sessions/"+session.ID, `{
		"agent_options":{"codex":{"run_dangerously":true}}
	}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response sessionResponse
	decodeJSON(t, rec, &response)
	responseOptions, ok := response.AgentOptions.(map[string]any)
	if !ok {
		t.Fatalf("expected response options map, got %#v", response.AgentOptions)
	}
	codexOptions, ok := responseOptions["codex"].(map[string]any)
	if !ok || codexOptions["run_dangerously"] != true {
		t.Fatalf("expected run_dangerously option, got %#v", response.AgentOptions)
	}

	updated, err := dbStore.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	options := decodeTestAgentOptions(t, updated.AgentOptions)
	if options["codex"]["run_dangerously"] != true {
		t.Fatalf("expected persisted run_dangerously option, got %#v", options)
	}
}

func TestUpdateCodexSessionAgentOptionsClearsDangerousModeToAsk(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:        "Codex",
		AgentType:    "codex",
		AgentOptions: json.RawMessage(`{"codex":{"run_dangerously":true}}`),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	rec := patchJSON(handler, "/api/sessions/"+session.ID, `{
		"agent_options":{"codex":{"run_dangerously":false}}
	}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response sessionResponse
	decodeJSON(t, rec, &response)
	responseOptions, ok := response.AgentOptions.(map[string]any)
	if !ok {
		t.Fatalf("expected response options map, got %#v", response.AgentOptions)
	}
	responseCodex, ok := responseOptions["codex"].(map[string]any)
	if !ok || responseCodex["permission_policy"] != "ask" {
		t.Fatalf("expected codex permission policy ask, got %#v", response.AgentOptions)
	}

	updated, err := dbStore.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	options := decodeTestAgentOptions(t, updated.AgentOptions)
	if options["codex"]["permission_policy"] != "ask" {
		t.Fatalf("expected persisted codex permission policy ask, got %#v", options)
	}
}

func TestUpdateSessionPinPersistsAndEmitsDurableEvent(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{Title: "Important", AgentType: "fake"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	originalUpdatedAt := session.UpdatedAt

	rec := patchJSON(handler, "/api/sessions/"+session.ID, `{"pinned":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response sessionResponse
	decodeJSON(t, rec, &response)
	if response.PinnedAt == nil {
		t.Fatal("expected pinned_at in response")
	}
	updated, err := dbStore.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if updated.PinnedAt == nil {
		t.Fatal("expected persisted pin")
	}
	if !updated.UpdatedAt.Equal(originalUpdatedAt) {
		t.Fatalf("expected pin not to change updated_at: %s != %s", updated.UpdatedAt, originalUpdatedAt)
	}

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	assertEventTypes(t, events, []string{"session.pin.updated"})
	var payload map[string]any
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("decode pin event: %v", err)
	}
	if payload["pinned_at"] != *response.PinnedAt {
		t.Fatalf("expected event pin %q, got %#v", *response.PinnedAt, payload["pinned_at"])
	}

	unpinRec := patchJSON(handler, "/api/sessions/"+session.ID, `{"pinned":false}`)
	if unpinRec.Code != http.StatusOK {
		t.Fatalf("expected unpin status %d, got %d with body %s", http.StatusOK, unpinRec.Code, unpinRec.Body.String())
	}
	decodeJSON(t, unpinRec, &response)
	if response.PinnedAt != nil {
		t.Fatalf("expected unpinned response, got %v", response.PinnedAt)
	}
}

func TestUpdateSessionPinRejectsArchivedSession(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{Title: "Archived", AgentType: "fake"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := dbStore.ArchiveSession(ctx, store.ArchiveSessionParams{ID: session.ID}); err != nil {
		t.Fatalf("archive session: %v", err)
	}

	rec := patchJSON(handler, "/api/sessions/"+session.ID, `{"pinned":true}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

func TestUpdateSessionRuntimeAgentOptionsPersistsAndPreservesPermissions(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:        "Codex",
		AgentType:    "codex",
		AgentOptions: json.RawMessage(`{"codex":{"permission_policy":"deny"}}`),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	rec := putJSON(handler, "/api/sessions/"+session.ID+"/agent-options/runtime", `{
		"options":{"codex":{
			"model":"gpt-5.6",
			"reasoning_effort":"xhigh",
			"fast_mode":true,
			"planning_mode":false
		}}
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response updateSessionRuntimeAgentOptionsResponse
	decodeJSON(t, rec, &response)
	if !response.Applied {
		t.Fatal("expected runtime options update to apply")
	}
	if response.Session.EventCount != 1 || response.Session.LastEventSeq != 1 {
		t.Fatalf("expected response to include settings event counters, got %#v", response.Session)
	}

	updated, err := dbStore.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	options := decodeTestAgentOptions(t, updated.AgentOptions)["codex"]
	if options["permission_policy"] != "deny" || options["model"] != "gpt-5.6" || options["reasoning_effort"] != "xhigh" {
		t.Fatalf("expected permission and runtime options, got %#v", options)
	}
	if options["fast_mode"] != true || options["planning_mode"] != false {
		t.Fatalf("expected explicit runtime toggles, got %#v", options)
	}

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	assertEventTypes(t, events, []string{"session.agent_options.updated"})
	var payload map[string]any
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("decode settings event payload: %v", err)
	}
	payloadOptions, ok := payload["agent_options"].(map[string]any)
	if !ok {
		t.Fatalf("expected event agent options, got %#v", payload)
	}
	payloadCodex, ok := payloadOptions["codex"].(map[string]any)
	if !ok || payloadCodex["model"] != "gpt-5.6" {
		t.Fatalf("expected event runtime snapshot, got %#v", payloadOptions)
	}
}

func TestInitializeSessionRuntimeAgentOptionsKeepsExistingServerSelection(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:     "Codex",
		AgentType: "codex",
		AgentOptions: json.RawMessage(`{"codex":{
			"permission_policy":"ask",
			"model":"gpt-5.6",
			"reasoning_effort":"high",
			"fast_mode":false,
			"planning_mode":true
		}}`),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	rec := putJSON(handler, "/api/sessions/"+session.ID+"/agent-options/runtime", `{
		"initialize_if_absent":true,
		"options":{"codex":{
			"model":"gpt-5-mini",
			"reasoning_effort":"medium",
			"fast_mode":true,
			"planning_mode":false
		}}
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response updateSessionRuntimeAgentOptionsResponse
	decodeJSON(t, rec, &response)
	if response.Applied {
		t.Fatal("expected conditional initialization to keep existing server options")
	}
	responseOptions, ok := response.Session.AgentOptions.(map[string]any)
	if !ok {
		t.Fatalf("expected response options map, got %#v", response.Session.AgentOptions)
	}
	responseCodex, ok := responseOptions["codex"].(map[string]any)
	if !ok || responseCodex["model"] != "gpt-5.6" || responseCodex["planning_mode"] != true {
		t.Fatalf("expected existing server runtime options, got %#v", response.Session.AgentOptions)
	}
	if events := listIntegrationEvents(t, ctx, dbStore, session.ID); len(events) != 0 {
		t.Fatalf("expected skipped initialization not to append an event, got %#v", events)
	}
}

func TestPermissionUpdatePreservesSessionRuntimeAgentOptions(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:     "Codex",
		AgentType: "codex",
		AgentOptions: json.RawMessage(`{"codex":{
			"permission_policy":"ask",
			"model":"gpt-5.6",
			"reasoning_effort":"xhigh",
			"fast_mode":true,
			"planning_mode":true
		}}`),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	rec := patchJSON(handler, "/api/sessions/"+session.ID, `{
		"agent_options":{"codex":{"permission_policy":"deny"}}
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	updated, err := dbStore.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	options := decodeTestAgentOptions(t, updated.AgentOptions)["codex"]
	if options["permission_policy"] != "deny" || options["model"] != "gpt-5.6" || options["fast_mode"] != true {
		t.Fatalf("expected permission update to preserve runtime options, got %#v", options)
	}
	assertEventTypes(t, listIntegrationEvents(t, ctx, dbStore, session.ID), []string{"session.agent_options.updated"})
}

func TestUpdateSessionRuntimeAgentOptionsRejectsWrongProvider(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{Title: "Codex", AgentType: "codex"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	rec := putJSON(handler, "/api/sessions/"+session.ID+"/agent-options/runtime", `{
		"options":{"claude":{"model":"opus","effort":"high","planning_mode":true}}
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "codex options require a codex session")
}

func TestUpdateSessionTitleReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	_, _, _, handler := newIntegrationAPI(t, ctx, fake.New())

	rec := patchJSON(handler, "/api/sessions/sess_missing", `{"title":"Missing"}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusNotFound, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "session not found")
}

func TestUpdateSessionTitleRejectsMalformedJSON(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session := createIntegrationSession(t, ctx, dbStore)

	rec := patchJSON(handler, "/api/sessions/"+session.ID, `{"title"`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "invalid JSON body")
}

func TestArchiveSessionSetsArchivedAtAndHidesFromList(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session := createIntegrationSession(t, ctx, dbStore)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/archive", ``)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response sessionResponse
	decodeJSON(t, rec, &response)
	if response.ID != session.ID {
		t.Fatalf("expected archived session id %q, got %q", session.ID, response.ID)
	}
	if response.ArchivedAt == nil {
		t.Fatal("expected archived_at in response")
	}
	if response.EventCount != 2 || response.LastEventSeq != 2 {
		t.Fatalf("expected response to include archive event counters, got %#v", response)
	}
	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	assertEventTypes(t, events, []string{"schedule.archived", "session.archived"})
	payload := decodeEventPayload(t, events[1])
	if payload["archived_at"] != *response.ArchivedAt {
		t.Fatalf("expected archive event timestamp %q, got %#v", *response.ArchivedAt, payload["archived_at"])
	}

	updated, err := dbStore.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if updated.ArchivedAt == nil {
		t.Fatal("expected persisted archived_at")
	}

	sessions, err := dbStore.ListSessions(ctx, store.ListSessionsParams{})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected archived session to be hidden from list, got %#v", sessions)
	}
}

func TestArchiveRunningSessionReturnsConflict(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session := createIntegrationSession(t, ctx, dbStore)
	if _, err := dbStore.UpdateSessionStatus(ctx, store.UpdateSessionStatusParams{
		ID:     session.ID,
		Status: store.SessionStatusRunning,
	}); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/archive", ``)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusConflict, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "running session cannot be archived")

	updated, err := dbStore.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if updated.ArchivedAt != nil {
		t.Fatalf("expected running session not to be archived, got %s", updated.ArchivedAt)
	}
}

func TestRestoreSessionClearsArchivedAtAndReturnsToList(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session := createIntegrationSession(t, ctx, dbStore)
	if _, err := dbStore.ArchiveSession(ctx, store.ArchiveSessionParams{ID: session.ID}); err != nil {
		t.Fatalf("archive session: %v", err)
	}

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/restore", ``)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response sessionResponse
	decodeJSON(t, rec, &response)
	if response.ID != session.ID {
		t.Fatalf("expected restored session id %q, got %q", session.ID, response.ID)
	}
	if response.ArchivedAt != nil {
		t.Fatalf("expected restored response archived_at nil, got %v", response.ArchivedAt)
	}
	if response.EventCount != 1 || response.LastEventSeq != 1 {
		t.Fatalf("expected response to include restore event counters, got %#v", response)
	}
	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	assertEventTypes(t, events, []string{"session.restored"})
	payload := decodeEventPayload(t, events[0])
	if payload["archived_at"] != nil {
		t.Fatalf("expected restore event archived_at null, got %#v", payload["archived_at"])
	}

	updated, err := dbStore.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if updated.ArchivedAt != nil {
		t.Fatalf("expected persisted restored archived_at nil, got %v", updated.ArchivedAt)
	}

	sessions, err := dbStore.ListSessions(ctx, store.ListSessionsParams{})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != session.ID {
		t.Fatalf("expected restored session in list, got %#v", sessions)
	}
}

func TestMessageSubmissionReturnsUnavailableForRegisteredUnavailableAgent(t *testing.T) {
	ctx := context.Background()
	codexAgent := availabilityAgent{agentType: "codex", availableErr: agents.ErrUnavailable}
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, codexAgent)
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:     "Codex run",
		AgentType: "codex",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Inspect this repo"}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "agent unavailable")

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	if len(events) != 0 {
		t.Fatalf("expected no events to be appended, got %#v", events)
	}
}

func TestMessageSubmissionPassesConfiguredWorkdirToAgent(t *testing.T) {
	ctx := context.Background()
	agent := newBlockingAgent()
	workdir := canonicalPath(t, t.TempDir())
	dbStore, _, _, handler := newIntegrationAPIWithWorkdir(t, ctx, workdir, agent)
	session := createIntegrationSession(t, ctx, dbStore)
	t.Cleanup(agent.release)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Inspect this repo"}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	select {
	case input := <-agent.started:
		if input.Workdir != workdir {
			t.Fatalf("expected workdir %q, got %q", workdir, input.Workdir)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("context ended before agent started")
	}
}

func TestMessageSubmissionUsesUpdatedSessionWorkspace(t *testing.T) {
	ctx := context.Background()
	agent := newBlockingAgent()
	defaultWorkdir := canonicalPath(t, t.TempDir())
	initialWorkdir := filepath.Join(defaultWorkdir, "initial")
	updatedWorkdir := filepath.Join(defaultWorkdir, "updated")
	for _, path := range []string{initialWorkdir, updatedWorkdir} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatalf("create session workspace: %v", err)
		}
	}
	dbStore, _, _, handler := newIntegrationAPIWithWorkdir(t, ctx, defaultWorkdir, agent)
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:         "Workspace run",
		AgentType:     "fake",
		WorkspacePath: initialWorkdir,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(agent.release)

	updateRec := patchJSON(handler, "/api/sessions/"+session.ID, `{"workspace_path":`+quoteJSON(updatedWorkdir)+`}`)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("expected workspace update status %d, got %d with body %s", http.StatusOK, updateRec.Code, updateRec.Body.String())
	}

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Inspect this repo"}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	select {
	case input := <-agent.started:
		if input.Workdir != updatedWorkdir {
			t.Fatalf("expected workdir %q, got %q", updatedWorkdir, input.Workdir)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("context ended before agent started")
	}
}

func TestMessageSubmissionPassesCodexOptionsToAgentMetadata(t *testing.T) {
	ctx := context.Background()
	agent := newBlockingAgent()
	agent.agentType = "codex"
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, agent)
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:     "Codex run",
		AgentType: "codex",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(agent.release)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{
		"content":"Inspect this repo",
		"agent_options":{
			"codex":{
				"model":"gpt-5.5",
				"reasoning_effort":"xhigh",
				"fast_mode":true,
				"planning_mode":true,
				"service_tier":"priority"
			}
		}
	}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	select {
	case input := <-agent.started:
		if input.Metadata["agent_type"] != "codex" {
			t.Fatalf("expected codex agent metadata, got %#v", input.Metadata)
		}
		options, ok := input.Metadata["codex_options"].(map[string]any)
		if !ok {
			t.Fatalf("expected codex options metadata, got %#v", input.Metadata["codex_options"])
		}
		assertMetadataValue(t, options, "model", "gpt-5.5")
		assertMetadataValue(t, options, "reasoning_effort", "xhigh")
		assertMetadataValue(t, options, "service_tier", "priority")
		assertMetadataValue(t, options, "fast_mode", true)
		assertMetadataValue(t, options, "planning_mode", true)
	case <-time.After(2 * time.Second):
		t.Fatal("context ended before agent started")
	}
}

func TestMessageSubmissionPassesSessionCodexOptionsToAgentMetadata(t *testing.T) {
	ctx := context.Background()
	agent := newBlockingAgent()
	agent.agentType = "codex"
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, agent)
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:     "Codex run",
		AgentType: "codex",
		AgentOptions: json.RawMessage(`{"codex":{
			"run_dangerously":true,
			"model":"gpt-5.6",
			"reasoning_effort":"xhigh",
			"fast_mode":true,
			"planning_mode":true
		}}`),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(agent.release)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Inspect this repo"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	select {
	case input := <-agent.started:
		options, ok := input.Metadata["codex_options"].(map[string]any)
		if !ok {
			t.Fatalf("expected codex options metadata, got %#v", input.Metadata["codex_options"])
		}
		assertMetadataValue(t, options, "run_dangerously", true)
		assertMetadataValue(t, options, "model", "gpt-5.6")
		assertMetadataValue(t, options, "reasoning_effort", "xhigh")
		assertMetadataValue(t, options, "fast_mode", true)
		assertMetadataValue(t, options, "planning_mode", true)
	case <-time.After(2 * time.Second):
		t.Fatal("context ended before agent started")
	}
}

func TestMessageSubmissionPassesSessionClaudeOptionsToAgentMetadata(t *testing.T) {
	ctx := context.Background()
	agent := newBlockingAgent()
	agent.agentType = "claude"
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, agent)
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:        "Claude run",
		AgentType:    "claude",
		AgentOptions: json.RawMessage(`{"claude":{"run_dangerously":true}}`),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(agent.release)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Inspect this repo"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	select {
	case input := <-agent.started:
		options, ok := input.Metadata["claude_options"].(map[string]any)
		if !ok {
			t.Fatalf("expected claude options metadata, got %#v", input.Metadata["claude_options"])
		}
		assertMetadataValue(t, options, "run_dangerously", true)
	case <-time.After(2 * time.Second):
		t.Fatal("context ended before agent started")
	}
}

func TestMessageSubmissionPassesClaudeOptionsToAgentMetadata(t *testing.T) {
	ctx := context.Background()
	agent := newBlockingAgent()
	agent.agentType = "claude"
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, agent)
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:     "Claude run",
		AgentType: "claude",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(agent.release)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{
		"content":"Inspect this repo",
		"agent_options":{"claude":{"model":"opus","effort":"high","planning_mode":true}}
	}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	select {
	case input := <-agent.started:
		options, ok := input.Metadata["claude_options"].(map[string]any)
		if !ok {
			t.Fatalf("expected claude options metadata, got %#v", input.Metadata["claude_options"])
		}
		assertMetadataValue(t, options, "model", "opus")
		assertMetadataValue(t, options, "effort", "high")
		assertMetadataValue(t, options, "planning_mode", true)
		assertMetadataValue(t, options, "permission_mode", "plan")
	case <-time.After(2 * time.Second):
		t.Fatal("context ended before agent started")
	}
}

func TestMessageSubmissionPassesPiOptionsToAgentMetadata(t *testing.T) {
	ctx := context.Background()
	agent := newBlockingAgent()
	agent.agentType = "pi"
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, agent)
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:     "Pi run",
		AgentType: "pi",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(agent.release)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{
		"content":"Inspect this repo",
		"agent_options":{"pi":{"model":"anthropic/claude-sonnet-4-5","thinking_level":"high"}}
	}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	select {
	case input := <-agent.started:
		options, ok := input.Metadata["pi_options"].(map[string]any)
		if !ok {
			t.Fatalf("expected pi options metadata, got %#v", input.Metadata["pi_options"])
		}
		assertMetadataValue(t, options, "model", "anthropic/claude-sonnet-4-5")
		assertMetadataValue(t, options, "thinking_level", "high")
	case <-time.After(2 * time.Second):
		t.Fatal("context ended before agent started")
	}
}

func TestMessageSubmissionPassesProviderSessionIDToCodexAgent(t *testing.T) {
	ctx := context.Background()
	agent := newBlockingAgent()
	agent.agentType = "codex"
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, agent)
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:     "Codex run",
		AgentType: "codex",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := dbStore.SetSessionProviderSessionID(ctx, store.SetSessionProviderSessionIDParams{
		ID:                session.ID,
		ProviderSessionID: "thread_existing",
	}); err != nil {
		t.Fatalf("set provider session id: %v", err)
	}
	t.Cleanup(agent.release)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Continue"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	select {
	case input := <-agent.started:
		if input.ProviderSessionID != "thread_existing" {
			t.Fatalf("expected provider session id thread_existing, got %q", input.ProviderSessionID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("context ended before agent started")
	}
}

func TestCodexRunStartedPersistsProviderSessionID(t *testing.T) {
	ctx := context.Background()
	agent := codexThreadAgent{threadID: "thread_created"}
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, agent)
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:     "Codex run",
		AgentType: "codex",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Start"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	waitFor(t, func() bool {
		session, err := dbStore.GetSession(ctx, session.ID)
		return err == nil && session.Status == store.SessionStatusIdle
	})

	updated, err := dbStore.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if updated.ProviderSessionID != "thread_created" {
		t.Fatalf("expected provider session id thread_created, got %q", updated.ProviderSessionID)
	}
}

func TestClaudeRunStartedPersistsProviderSessionID(t *testing.T) {
	ctx := context.Background()
	agent := claudeSessionAgent{sessionID: "2fe74369-4b15-49f9-8025-517ed6e52fed"}
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, agent)
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:     "Claude run",
		AgentType: "claude",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Start"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	waitFor(t, func() bool {
		session, err := dbStore.GetSession(ctx, session.ID)
		return err == nil && session.Status == store.SessionStatusIdle
	})

	updated, err := dbStore.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if updated.ProviderSessionID != "2fe74369-4b15-49f9-8025-517ed6e52fed" {
		t.Fatalf("expected provider session id, got %q", updated.ProviderSessionID)
	}
}

func TestOpenCodeRunStartedPersistsProviderSessionID(t *testing.T) {
	ctx := context.Background()
	agent := opencodeSessionAgent{sessionID: "ses_created"}
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, agent)
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:     "OpenCode run",
		AgentType: "opencode",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Start"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	waitFor(t, func() bool {
		session, err := dbStore.GetSession(ctx, session.ID)
		return err == nil && session.Status == store.SessionStatusIdle
	})

	updated, err := dbStore.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if updated.ProviderSessionID != "ses_created" {
		t.Fatalf("expected provider session id ses_created, got %q", updated.ProviderSessionID)
	}
}

func TestPiRunStartedPersistsProviderSessionID(t *testing.T) {
	ctx := context.Background()
	agent := piSessionAgent{sessionID: "pi_created"}
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, agent)
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:     "Pi run",
		AgentType: "pi",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Start"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	waitFor(t, func() bool {
		session, err := dbStore.GetSession(ctx, session.ID)
		return err == nil && session.Status == store.SessionStatusIdle
	})

	updated, err := dbStore.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if updated.ProviderSessionID != "pi_created" {
		t.Fatalf("expected provider session id pi_created, got %q", updated.ProviderSessionID)
	}
}

func TestClearSessionClearsProviderSessionID(t *testing.T) {
	for _, agentType := range []string{"codex", "opencode"} {
		t.Run(agentType, func(t *testing.T) {
			ctx := context.Background()
			agent := newBlockingAgent()
			agent.agentType = agentType
			dbStore, _, _, handler := newIntegrationAPI(t, ctx, agent)
			session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
				Title:     agentType + " run",
				AgentType: agentType,
			})
			if err != nil {
				t.Fatalf("create session: %v", err)
			}
			if _, err := dbStore.SetSessionProviderSessionID(ctx, store.SetSessionProviderSessionIDParams{
				ID:                session.ID,
				ProviderSessionID: "provider_session_old",
			}); err != nil {
				t.Fatalf("set provider session id: %v", err)
			}
			if _, err := dbStore.UpdateSessionStatus(ctx, store.UpdateSessionStatusParams{
				ID:     session.ID,
				Status: store.SessionStatusFailed,
			}); err != nil {
				t.Fatalf("mark failed: %v", err)
			}

			rec := postJSON(handler, "/api/sessions/"+session.ID+"/clear", ``)
			if rec.Code != http.StatusAccepted {
				t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
			}
			var response submitMessageResponse
			decodeJSON(t, rec, &response)
			if response.Status != string(store.SessionStatusIdle) {
				t.Fatalf("expected idle response status, got %q", response.Status)
			}

			updated, err := dbStore.GetSession(ctx, session.ID)
			if err != nil {
				t.Fatalf("get session: %v", err)
			}
			if updated.ProviderSessionID != "" {
				t.Fatalf("expected provider session id to be cleared, got %q", updated.ProviderSessionID)
			}
			if updated.Status != store.SessionStatusIdle {
				t.Fatalf("expected session status idle, got %q", updated.Status)
			}

			events := listIntegrationEvents(t, ctx, dbStore, session.ID)
			assertEventTypes(t, events, []string{
				"session.action.completed",
				"session.status.updated",
			})
			assertPayloadAction(t, events[0], "clear")
			assertPayloadStatus(t, events[1], store.SessionStatusIdle)

			select {
			case input := <-agent.started:
				t.Fatalf("expected clear not to start %s agent, got %#v", agentType, input)
			default:
			}
		})
	}
}

func TestCompactCodexSessionStartsActionRun(t *testing.T) {
	ctx := context.Background()
	agent := newBlockingAgent()
	agent.agentType = "codex"
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, agent)
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:     "Codex run",
		AgentType: "codex",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := dbStore.SetSessionProviderSessionID(ctx, store.SetSessionProviderSessionIDParams{
		ID:                session.ID,
		ProviderSessionID: "thread_existing",
	}); err != nil {
		t.Fatalf("set provider session id: %v", err)
	}
	t.Cleanup(agent.release)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/compact", ``)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	select {
	case input := <-agent.started:
		if input.Action != agents.AgentActionCompact {
			t.Fatalf("expected compact action, got %q", input.Action)
		}
		if input.ProviderSessionID != "thread_existing" {
			t.Fatalf("expected provider session id thread_existing, got %q", input.ProviderSessionID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("context ended before agent started")
	}

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	assertEventTypes(t, events, []string{"session.action.completed", "session.status.updated"})
	assertPayloadAction(t, events[0], "compact")
	assertPayloadStatus(t, events[1], store.SessionStatusRunning)

	agent.release()
	waitFor(t, func() bool {
		session, err := dbStore.GetSession(ctx, session.ID)
		return err == nil && session.Status == store.SessionStatusIdle
	})
}

func TestCompactCodexSessionWithoutProviderSessionIDReturnsConflict(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, codexThreadAgent{threadID: "thread_new"})
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:     "Codex run",
		AgentType: "codex",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/compact", ``)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusConflict, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "session has no codex thread to compact")

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	if len(events) != 0 {
		t.Fatalf("expected no events to be appended, got %#v", events)
	}
}

func TestClearSessionRejectsUnsupportedAgent(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session := createIntegrationSession(t, ctx, dbStore)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/clear", ``)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "session clear requires a codex or opencode session")
}

func TestMessageSubmissionRejectsCodexOptionsForNonCodexSession(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session := createIntegrationSession(t, ctx, dbStore)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{
		"content":"Inspect this repo",
		"agent_options":{"codex":{"model":"gpt-5.5"}}
	}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "codex options require a codex session")

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	if len(events) != 0 {
		t.Fatalf("expected no events to be appended, got %#v", events)
	}
}

func TestSuccessfulFakeAgentRunCompletesSessionAndIsVisibleThroughHistory(t *testing.T) {
	ctx := context.Background()
	dbStore, _, runManager, handler := newIntegrationAPI(t, ctx, fake.New())

	createRec := postJSON(handler, "/api/sessions", `{"agent_type":"fake","title":"Fake run"}`)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected create status %d, got %d with body %s", http.StatusCreated, createRec.Code, createRec.Body.String())
	}

	var createResponse createSessionResponse
	decodeJSON(t, createRec, &createResponse)

	messageRec := postJSON(handler, "/api/sessions/"+createResponse.SessionID+"/messages", `{"content":"Inspect this repo"}`)
	if messageRec.Code != http.StatusAccepted {
		t.Fatalf("expected message status %d, got %d with body %s", http.StatusAccepted, messageRec.Code, messageRec.Body.String())
	}

	waitFor(t, func() bool {
		session, err := dbStore.GetSession(ctx, createResponse.SessionID)
		return err == nil && session.Status == store.SessionStatusIdle
	})
	waitFor(t, func() bool {
		return !runManager.Active(createResponse.SessionID)
	})

	events := listIntegrationEvents(t, ctx, dbStore, createResponse.SessionID)
	assertEventTypes(t, events, []string{
		"user.message.completed",
		"session.status.updated",
		"agent.run.started",
		"agent.message.completed",
		"agent.run.completed",
		"session.status.updated",
	})
	assertTerminalRunEventCount(t, events, 1)
	assertPayloadStatus(t, events[1], store.SessionStatusRunning)
	assertPayloadText(t, events[3], "Fake agent completed the task.")
	assertPayloadStatus(t, events[5], store.SessionStatusIdle)

	historyReq := httptest.NewRequest(http.MethodGet, "/api/sessions/"+createResponse.SessionID+"/events?after_seq=0", nil)
	historyRec := httptest.NewRecorder()
	handler.ServeHTTP(historyRec, historyReq)

	if historyRec.Code != http.StatusOK {
		t.Fatalf("expected history status %d, got %d with body %s", http.StatusOK, historyRec.Code, historyRec.Body.String())
	}

	var historyResponse eventHistoryResponse
	decodeJSON(t, historyRec, &historyResponse)
	if len(historyResponse.Events) != len(events) {
		t.Fatalf("expected %d history events, got %d", len(events), len(historyResponse.Events))
	}
	if historyResponse.Events[5].Type != "session.status.updated" {
		t.Fatalf("expected final history event session.status.updated, got %q", historyResponse.Events[5].Type)
	}
}

func TestFakeAgentErrorEmitsFailedEventAndMarksSessionFailed(t *testing.T) {
	ctx := context.Background()
	dbStore, _, runManager, handler := newIntegrationAPI(t, ctx, fake.New(fake.WithError(errors.New("planned failure"))))
	session := createIntegrationSession(t, ctx, dbStore)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Fail this task"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	waitFor(t, func() bool {
		session, err := dbStore.GetSession(ctx, session.ID)
		return err == nil && session.Status == store.SessionStatusFailed
	})
	waitFor(t, func() bool {
		return !runManager.Active(session.ID)
	})

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	assertEventTypes(t, events, []string{
		"user.message.completed",
		"session.status.updated",
		"agent.run.started",
		"agent.run.failed",
		"session.status.updated",
	})
	assertTerminalRunEventCount(t, events, 1)
	assertPayloadStatus(t, events[1], store.SessionStatusRunning)
	if events[3].Status != store.EventStatusFailed {
		t.Fatalf("expected failed status, got %q", events[3].Status)
	}
	assertPayloadError(t, events[3], "planned failure")
	assertPayloadStatus(t, events[4], store.SessionStatusFailed)
}

func TestCancelRunningFakeAgentMarksSessionCancelledAndCleansUpRun(t *testing.T) {
	ctx := context.Background()
	stepBarrier := make(chan struct{})
	dbStore, _, runManager, handler := newIntegrationAPI(t, ctx, fake.New(fake.WithStepBarrier(stepBarrier)))
	session := createIntegrationSession(t, ctx, dbStore)

	messageRec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Cancel this task"}`)
	if messageRec.Code != http.StatusAccepted {
		t.Fatalf("expected message status %d, got %d with body %s", http.StatusAccepted, messageRec.Code, messageRec.Body.String())
	}

	waitFor(t, func() bool {
		return runManager.Active(session.ID) && hasEventType(t, ctx, dbStore, session.ID, "agent.run.started")
	})

	cancelRec := postJSON(handler, "/api/sessions/"+session.ID+"/cancel", `{"source":"web_ui","reason":"stop_button"}`)
	if cancelRec.Code != http.StatusAccepted {
		t.Fatalf("expected cancel status %d, got %d with body %s", http.StatusAccepted, cancelRec.Code, cancelRec.Body.String())
	}

	var cancelResponse cancelSessionResponse
	decodeJSON(t, cancelRec, &cancelResponse)
	if cancelResponse.SessionID != session.ID {
		t.Fatalf("expected session_id %q, got %q", session.ID, cancelResponse.SessionID)
	}
	if cancelResponse.Status != "cancelling" {
		t.Fatalf("expected cancelling status, got %q", cancelResponse.Status)
	}

	waitFor(t, func() bool {
		session, err := dbStore.GetSession(ctx, session.ID)
		return err == nil && session.Status == store.SessionStatusIdle
	})
	waitFor(t, func() bool {
		return !runManager.Active(session.ID)
	})

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	assertEventTypes(t, events, []string{
		"user.message.completed",
		"session.status.updated",
		"agent.run.started",
		"agent.run.cancelled",
		"session.status.updated",
	})
	assertTerminalRunEventCount(t, events, 1)
	assertPayloadStatus(t, events[1], store.SessionStatusRunning)
	assertPayloadStatus(t, events[4], store.SessionStatusIdle)
	cancelPayload := decodeEventPayload(t, events[3])
	if cancelPayload["cancel_source"] != "web_ui" || cancelPayload["cancel_reason"] != "stop_button" {
		t.Fatalf("unexpected cancellation payload %#v", cancelPayload)
	}
	if hasEvent(events, "agent.run.completed") {
		t.Fatal("expected cancelled run not to emit agent.run.completed")
	}
}

func TestAnswerUserInputRequestPersistsAnswerAndCompletesRun(t *testing.T) {
	ctx := context.Background()
	agent := userInputAgent{}
	dbStore, _, runManager, handler := newIntegrationAPI(t, ctx, agent)
	session := createIntegrationSession(t, ctx, dbStore)

	messageRec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Ask me"}`)
	if messageRec.Code != http.StatusAccepted {
		t.Fatalf("expected message status %d, got %d with body %s", http.StatusAccepted, messageRec.Code, messageRec.Body.String())
	}
	waitFor(t, func() bool {
		return hasEventType(t, ctx, dbStore, session.ID, "agent.input.requested")
	})

	answerRec := postJSON(handler, "/api/sessions/"+session.ID+"/requests/call_test/answer", `{
		"answers": {
			"question_test": {
				"answers": ["Beta"]
			}
		}
	}`)
	if answerRec.Code != http.StatusAccepted {
		t.Fatalf("expected answer status %d, got %d with body %s", http.StatusAccepted, answerRec.Code, answerRec.Body.String())
	}

	waitFor(t, func() bool {
		session, err := dbStore.GetSession(ctx, session.ID)
		return err == nil && session.Status == store.SessionStatusIdle
	})
	waitFor(t, func() bool {
		return !runManager.Active(session.ID)
	})

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	assertEventTypes(t, events, []string{
		"user.message.completed",
		"session.status.updated",
		"agent.run.started",
		"agent.input.requested",
		"agent.input.answered",
		"agent.message.completed",
		"agent.run.completed",
		"session.status.updated",
	})
	assertPayloadStatus(t, events[1], store.SessionStatusRunning)
	assertPayloadStatus(t, events[7], store.SessionStatusIdle)
	answerPayload := decodeEventPayload(t, events[4])
	if answerPayload["request_id"] != "call_test" {
		t.Fatalf("expected answered request id call_test, got %#v", answerPayload["request_id"])
	}
	assertPayloadText(t, events[5], "Answer: Beta")
}

func TestAnswerUserInputRejectsInvalidOption(t *testing.T) {
	ctx := context.Background()
	agent := userInputAgent{}
	dbStore, _, runManager, handler := newIntegrationAPI(t, ctx, agent)
	session := createIntegrationSession(t, ctx, dbStore)
	t.Cleanup(func() {
		if runManager.Active(session.ID) {
			_ = runManager.Cancel(session.ID, runcontrol.Cancellation{Source: "internal", Reason: "test_cleanup"})
			waitFor(t, func() bool {
				session, err := dbStore.GetSession(ctx, session.ID)
				return err == nil && session.Status == store.SessionStatusIdle
			})
		}
	})

	messageRec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Ask me"}`)
	if messageRec.Code != http.StatusAccepted {
		t.Fatalf("expected message status %d, got %d with body %s", http.StatusAccepted, messageRec.Code, messageRec.Body.String())
	}
	waitFor(t, func() bool {
		return hasEventType(t, ctx, dbStore, session.ID, "agent.input.requested")
	})

	answerRec := postJSON(handler, "/api/sessions/"+session.ID+"/requests/call_test/answer", `{
		"answers": {
			"question_test": {
				"answers": ["Delta"]
			}
		}
	}`)
	if answerRec.Code != http.StatusBadRequest {
		t.Fatalf("expected answer status %d, got %d with body %s", http.StatusBadRequest, answerRec.Code, answerRec.Body.String())
	}
	assertErrorResponse(t, answerRec, `answer "question_test" is not a valid option`)
}

func TestResolvePermissionPersistsDecisionBeforeCompletingRun(t *testing.T) {
	ctx := context.Background()
	dbStore, _, runManager, handler := newIntegrationAPI(t, ctx, permissionAgent{})
	session := createIntegrationSession(t, ctx, dbStore)
	messageRec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Do it"}`)
	if messageRec.Code != http.StatusAccepted {
		t.Fatalf("submit: %d %s", messageRec.Code, messageRec.Body.String())
	}
	waitFor(t, func() bool { return hasEventType(t, ctx, dbStore, session.ID, "agent.permission.requested") })
	listRec := get(handler, "/api/sessions")
	var listed listSessionsResponse
	decodeJSON(t, listRec, &listed)
	if len(listed.Sessions) != 1 || listed.Sessions[0].PendingPermissionCount != 1 {
		t.Fatalf("expected one pending permission, got %#v", listed.Sessions)
	}
	resolveRec := postJSON(handler, "/api/sessions/"+session.ID+"/permissions/perm_test/resolve", `{"option_id":"allow-session"}`)
	if resolveRec.Code != http.StatusAccepted {
		t.Fatalf("resolve: %d %s", resolveRec.Code, resolveRec.Body.String())
	}
	waitFor(t, func() bool { return !runManager.Active(session.ID) })
	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	resolvedIndex, messageIndex := -1, -1
	for index, event := range events {
		if event.Type == "agent.permission.resolved" {
			resolvedIndex = index
		}
		if event.Type == "agent.message.completed" {
			messageIndex = index
		}
	}
	if resolvedIndex < 0 || messageIndex <= resolvedIndex {
		t.Fatalf("expected resolution before agent continuation, got %#v", events)
	}
	payload := decodeEventPayload(t, events[resolvedIndex])
	if payload["option_id"] != "allow-session" || payload["scope"] != "session" {
		t.Fatalf("unexpected resolution payload %#v", payload)
	}
}

func TestCancelUnknownSessionReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	_, _, _, handler := newIntegrationAPI(t, ctx, fake.New())

	rec := postJSON(handler, "/api/sessions/sess_missing/cancel", ``)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusNotFound, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "session not found")
}

func TestCancelIdleSessionReturnsConflict(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session := createIntegrationSession(t, ctx, dbStore)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/cancel", ``)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusConflict, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "session is not running")

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	if len(events) != 0 {
		t.Fatalf("expected no events, got %#v", events)
	}
}

func TestCancelIdleAfterCompletedRunReturnsConflict(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session := createIntegrationSession(t, ctx, dbStore)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/cancel", ``)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusConflict, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "session is not running")
}

func TestDuplicateCancelReturnsConflictAfterTerminalState(t *testing.T) {
	ctx := context.Background()
	stepBarrier := make(chan struct{})
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New(fake.WithStepBarrier(stepBarrier)))
	session := createIntegrationSession(t, ctx, dbStore)

	messageRec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Cancel twice"}`)
	if messageRec.Code != http.StatusAccepted {
		t.Fatalf("expected message status %d, got %d with body %s", http.StatusAccepted, messageRec.Code, messageRec.Body.String())
	}

	waitFor(t, func() bool {
		return hasEventType(t, ctx, dbStore, session.ID, "agent.run.started")
	})

	firstCancel := postJSON(handler, "/api/sessions/"+session.ID+"/cancel", ``)
	if firstCancel.Code != http.StatusAccepted {
		t.Fatalf("expected first cancel status %d, got %d with body %s", http.StatusAccepted, firstCancel.Code, firstCancel.Body.String())
	}

	waitFor(t, func() bool {
		session, err := dbStore.GetSession(ctx, session.ID)
		return err == nil && session.Status == store.SessionStatusIdle
	})

	secondCancel := postJSON(handler, "/api/sessions/"+session.ID+"/cancel", ``)
	if secondCancel.Code != http.StatusConflict {
		t.Fatalf("expected second cancel status %d, got %d with body %s", http.StatusConflict, secondCancel.Code, secondCancel.Body.String())
	}
	assertErrorResponse(t, secondCancel, "session is not running")
}

func TestCancelRunningSessionWithoutActiveRunFailsSession(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session := createIntegrationSession(t, ctx, dbStore)
	if _, err := dbStore.UpdateSessionStatus(ctx, store.UpdateSessionStatusParams{
		ID:     session.ID,
		Status: store.SessionStatusRunning,
	}); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/cancel", ``)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}
	var response submitMessageResponse
	decodeJSON(t, rec, &response)
	if response.SessionID != session.ID {
		t.Fatalf("expected session_id %q, got %q", session.ID, response.SessionID)
	}
	if response.Status != string(store.SessionStatusFailed) {
		t.Fatalf("expected failed status, got %q", response.Status)
	}

	updatedSession, err := dbStore.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if updatedSession.Status != store.SessionStatusFailed {
		t.Fatalf("expected failed status, got %q", updatedSession.Status)
	}

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	assertEventTypes(t, events, []string{"agent.run.failed", "session.status.updated"})
	assertPayloadStatus(t, events[1], store.SessionStatusFailed)
	assertTerminalRunEventCount(t, events, 1)
}

func TestCancelRejectsMalformedJSONBody(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session := createIntegrationSession(t, ctx, dbStore)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/cancel", `{"unterminated"`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "invalid JSON body")
}

func TestCancelRejectsUnsupportedProvenance(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session := createIntegrationSession(t, ctx, dbStore)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/cancel", `{"source":"unknown","reason":"mystery"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "cancel source and reason are unsupported")
}

func TestEmptyCancelRequestDefaultsToAPIProvenance(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)

	cancellation, ok := parseCancellationRequest(rec, req)

	if !ok || rec.Code != http.StatusOK {
		t.Fatalf("expected valid request, got ok=%t status=%d body=%s", ok, rec.Code, rec.Body.String())
	}
	if cancellation.Source != "api" || cancellation.Reason != "requested" {
		t.Fatalf("unexpected cancellation %#v", cancellation)
	}
}

func TestCancellationDetailsPreserveProviderPayload(t *testing.T) {
	event := withCancellationDetails(agents.AgentEvent{
		Type:    "agent.run.cancelled",
		Payload: map[string]any{"turn_id": "turn_one"},
	}, runcontrol.Cancellation{Source: "web_ui", Reason: "stop_button"})

	payload := event.Payload.(map[string]any)
	if payload["turn_id"] != "turn_one" || payload["cancel_source"] != "web_ui" || payload["cancel_reason"] != "stop_button" {
		t.Fatalf("unexpected cancellation payload %#v", payload)
	}
}

func TestMessageSubmissionToRunningSessionQueuesMessage(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session := createIntegrationSession(t, ctx, dbStore)

	if _, err := dbStore.UpdateSessionStatus(ctx, store.UpdateSessionStatusParams{
		ID:     session.ID,
		Status: store.SessionStatusRunning,
	}); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Second message"}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}
	var response submitMessageResponse
	decodeJSON(t, rec, &response)
	if response.AcceptedAs != "queued" || response.QueuedMessage == nil {
		t.Fatalf("expected queued response, got %#v", response)
	}
	if response.Status != string(store.SessionStatusRunning) {
		t.Fatalf("expected running status, got %q", response.Status)
	}

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	assertEventTypes(t, events, []string{"user.message.queued"})
	assertPayloadText(t, events[0], "Second message")

	queued, err := dbStore.ListQueuedMessages(ctx, session.ID)
	if err != nil {
		t.Fatalf("list queued messages: %v", err)
	}
	if len(queued) != 1 || queued[0].Content != "Second message" {
		t.Fatalf("expected queued message, got %#v", queued)
	}
}

func TestMessageSubmissionExplicitQueueDoesNotStartIdleSession(t *testing.T) {
	ctx := context.Background()
	agent := newBlockingAgent()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, agent)
	session := createIntegrationSession(t, ctx, dbStore)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Later","queue":true}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}
	var response submitMessageResponse
	decodeJSON(t, rec, &response)
	if response.AcceptedAs != "queued" || response.QueuedMessage == nil {
		t.Fatalf("expected queued response, got %#v", response)
	}
	if response.Status != string(store.SessionStatusIdle) {
		t.Fatalf("expected idle status, got %q", response.Status)
	}

	updated, err := dbStore.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if updated.Status != store.SessionStatusIdle {
		t.Fatalf("expected session to remain idle, got %q", updated.Status)
	}

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	assertEventTypes(t, events, []string{"user.message.queued"})
	assertPayloadText(t, events[0], "Later")

	select {
	case input := <-agent.started:
		t.Fatalf("expected explicit queue not to start agent, got %#v", input)
	default:
	}
}

func TestQueuedMessageRejectsAttachments(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session := createIntegrationSession(t, ctx, dbStore)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{
		"content":"Later",
		"queue":true,
		"attachments":[
			{
				"name":"diagram.png",
				"media_type":"image/png",
				"data_url":"data:image/png;base64,aGVsbG8=",
				"size_bytes":5
			}
		]
	}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "queued messages cannot include image attachments")

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	if len(events) != 0 {
		t.Fatalf("expected no events, got %#v", events)
	}
}

func TestQueuedMessagesCanBeListedAndRemoved(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session := createIntegrationSession(t, ctx, dbStore)

	queueRec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Later","queue":true}`)
	if queueRec.Code != http.StatusAccepted {
		t.Fatalf("expected queue status %d, got %d with body %s", http.StatusAccepted, queueRec.Code, queueRec.Body.String())
	}
	var queueResponse submitMessageResponse
	decodeJSON(t, queueRec, &queueResponse)
	if queueResponse.QueuedMessage == nil {
		t.Fatal("expected queued message response")
	}

	listRec := get(handler, "/api/sessions/"+session.ID+"/queued-messages")
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d with body %s", http.StatusOK, listRec.Code, listRec.Body.String())
	}
	var listResponse queuedMessagesResponse
	decodeJSON(t, listRec, &listResponse)
	if len(listResponse.Messages) != 1 || listResponse.Messages[0].Content != "Later" {
		t.Fatalf("expected one queued message, got %#v", listResponse.Messages)
	}

	removeRec := deleteRequest(handler, "/api/sessions/"+session.ID+"/queued-messages/"+queueResponse.QueuedMessage.ID)
	if removeRec.Code != http.StatusOK {
		t.Fatalf("expected remove status %d, got %d with body %s", http.StatusOK, removeRec.Code, removeRec.Body.String())
	}

	remaining, err := dbStore.ListQueuedMessages(ctx, session.ID)
	if err != nil {
		t.Fatalf("list queued messages: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected no pending queued messages, got %#v", remaining)
	}

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	assertEventTypes(t, events, []string{"user.message.queued", "user.message.queue.removed"})
	assertPayloadText(t, events[1], "Later")
}

func TestSuccessfulRunDispatchesNextQueuedMessage(t *testing.T) {
	ctx := context.Background()
	stepBarrier := make(chan struct{})
	dbStore, _, runManager, handler := newIntegrationAPI(t, ctx, fake.New(fake.WithStepBarrier(stepBarrier)))
	session := createIntegrationSession(t, ctx, dbStore)

	firstRec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"First"}`)
	if firstRec.Code != http.StatusAccepted {
		t.Fatalf("expected first status %d, got %d with body %s", http.StatusAccepted, firstRec.Code, firstRec.Body.String())
	}
	waitFor(t, func() bool {
		return runManager.Active(session.ID) && hasEventType(t, ctx, dbStore, session.ID, "agent.run.started")
	})

	queueRec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Second"}`)
	if queueRec.Code != http.StatusAccepted {
		t.Fatalf("expected queue status %d, got %d with body %s", http.StatusAccepted, queueRec.Code, queueRec.Body.String())
	}

	close(stepBarrier)

	waitFor(t, func() bool {
		events := listIntegrationEvents(t, ctx, dbStore, session.ID)
		return countEvents(events, "agent.run.completed") == 2
	})
	waitFor(t, func() bool {
		session, err := dbStore.GetSession(ctx, session.ID)
		return err == nil && session.Status == store.SessionStatusIdle && !runManager.Active(session.ID)
	})

	queued, err := dbStore.ListQueuedMessages(ctx, session.ID)
	if err != nil {
		t.Fatalf("list queued messages: %v", err)
	}
	if len(queued) != 0 {
		t.Fatalf("expected queued message to be sent, got %#v", queued)
	}

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	if userMessages := countEvents(events, "user.message.completed"); userMessages != 2 {
		t.Fatalf("expected two user messages, got %d", userMessages)
	}
	if !hasQueuedUserMessage(events) {
		t.Fatalf("expected queued user message to include queue item id, got %#v", events)
	}
}

func TestFailedRunLeavesQueuedMessagesPending(t *testing.T) {
	ctx := context.Background()
	agent := newFailingBlockingAgent(errors.New("planned failure"))
	dbStore, _, runManager, handler := newIntegrationAPI(t, ctx, agent)
	session := createIntegrationSession(t, ctx, dbStore)

	firstRec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"First"}`)
	if firstRec.Code != http.StatusAccepted {
		t.Fatalf("expected first status %d, got %d with body %s", http.StatusAccepted, firstRec.Code, firstRec.Body.String())
	}
	waitFor(t, func() bool {
		return runManager.Active(session.ID) && hasEventType(t, ctx, dbStore, session.ID, "agent.run.started")
	})

	queueRec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Second"}`)
	if queueRec.Code != http.StatusAccepted {
		t.Fatalf("expected queue status %d, got %d with body %s", http.StatusAccepted, queueRec.Code, queueRec.Body.String())
	}

	agent.release()

	waitFor(t, func() bool {
		session, err := dbStore.GetSession(ctx, session.ID)
		return err == nil && session.Status == store.SessionStatusFailed && !runManager.Active(session.ID)
	})

	queued, err := dbStore.ListQueuedMessages(ctx, session.ID)
	if err != nil {
		t.Fatalf("list queued messages: %v", err)
	}
	if len(queued) != 1 || queued[0].Content != "Second" {
		t.Fatalf("expected queued message to remain pending, got %#v", queued)
	}

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	if countEvents(events, "user.message.completed") != 1 {
		t.Fatalf("expected only first user message to run, got %d", countEvents(events, "user.message.completed"))
	}
	if countEvents(events, "agent.run.failed") != 1 {
		t.Fatalf("expected one failed run, got %#v", events)
	}
}

func TestMessageSubmissionToFailedSessionStartsNewRun(t *testing.T) {
	ctx := context.Background()
	agent := newBlockingAgent()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, agent)
	session := createIntegrationSession(t, ctx, dbStore)
	t.Cleanup(func() {
		agent.release()
		waitFor(t, func() bool {
			session, err := dbStore.GetSession(ctx, session.ID)
			return err == nil && session.Status == store.SessionStatusIdle
		})
	})

	failedSession, err := dbStore.UpdateSessionStatus(ctx, store.UpdateSessionStatusParams{
		ID:     session.ID,
		Status: store.SessionStatusFailed,
	})
	if err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if failedSession.CompletedAt == nil {
		t.Fatal("expected failure timestamp before submitting another message")
	}

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"Another message"}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	var response submitMessageResponse
	decodeJSON(t, rec, &response)
	if response.Status != string(store.SessionStatusRunning) {
		t.Fatalf("expected response status running, got %q", response.Status)
	}

	updatedSession, err := dbStore.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if updatedSession.Status != store.SessionStatusRunning {
		t.Fatalf("expected session status running, got %q", updatedSession.Status)
	}
	if updatedSession.CompletedAt != nil {
		t.Fatalf("expected completed_at to be cleared for new run, got %s", updatedSession.CompletedAt)
	}

	events := listIntegrationEvents(t, ctx, dbStore, session.ID)
	assertEventTypes(t, events, []string{"user.message.completed", "session.status.updated"})
	assertPayloadText(t, events[0], "Another message")
	assertPayloadStatus(t, events[1], store.SessionStatusRunning)
}

func TestMessageSubmissionToMissingSessionReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	_, _, _, handler := newIntegrationAPI(t, ctx, fake.New())

	rec := postJSON(handler, "/api/sessions/sess_missing/messages", `{"content":"Hello"}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusNotFound, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "session not found")
}

func TestMessageSubmissionRejectsEmptyContent(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session := createIntegrationSession(t, ctx, dbStore)

	rec := postJSON(handler, "/api/sessions/"+session.ID+"/messages", `{"content":"   "}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "content, attachments, or skills are required")
}

func TestWriteAPIsRejectMalformedJSON(t *testing.T) {
	ctx := context.Background()
	dbStore, _, _, handler := newIntegrationAPI(t, ctx, fake.New())
	session := createIntegrationSession(t, ctx, dbStore)

	for _, test := range []struct {
		name string
		path string
	}{
		{name: "create session", path: "/api/sessions"},
		{name: "submit message", path: "/api/sessions/" + session.ID + "/messages"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := postJSON(handler, test.path, `{"unterminated"`)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
			}
			assertErrorResponse(t, rec, "invalid JSON body")
		})
	}
}

func newIntegrationAPI(t *testing.T, ctx context.Context, agent agents.Agent) (*store.Store, *eventservice.Service, *runcontrol.Manager, http.Handler) {
	t.Helper()
	return newIntegrationAPIWithWorkdir(t, ctx, "", agent)
}

func newIntegrationAPIWithWorkdir(t *testing.T, ctx context.Context, workdir string, agent agents.Agent) (*store.Store, *eventservice.Service, *runcontrol.Manager, http.Handler) {
	t.Helper()
	return newIntegrationAPIWithWorkspaceRoots(t, ctx, workdir, nil, agent)
}

func newIntegrationAPIWithWorkspaceRoots(t *testing.T, ctx context.Context, workdir string, workspaceRoots []string, agent agents.Agent) (*store.Store, *eventservice.Service, *runcontrol.Manager, http.Handler) {
	t.Helper()

	dbStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := dbStore.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})

	events, err := eventservice.NewService(dbStore)
	if err != nil {
		t.Fatalf("new event service: %v", err)
	}

	registry, err := agents.NewRegistry(agent)
	if err != nil {
		t.Fatalf("new agent registry: %v", err)
	}

	runManager := runcontrol.NewManager()
	scheduleService := scheduler.New(dbStore, events)
	handler := NewRouter(Dependencies{
		Store:          dbStore,
		Events:         events,
		Agents:         registry,
		Runs:           runManager,
		Workdir:        workdir,
		WorkspaceRoots: workspaceRoots,
		Schedules:      scheduleService,
	})

	return dbStore, events, runManager, handler
}

func createIntegrationSession(t *testing.T, ctx context.Context, dbStore *store.Store) store.Session {
	t.Helper()

	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{
		Title:     "Test session",
		AgentType: "fake",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	return session
}

func postJSON(handler http.Handler, path string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func patchJSON(handler http.Handler, path string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func putJSON(handler http.Handler, path string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func get(handler http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func deleteRequest(handler http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func quoteJSON(value string) string {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(body)
}

func canonicalPath(t *testing.T, value string) string {
	t.Helper()
	evaluated, err := filepath.EvalSymlinks(value)
	if err != nil {
		t.Fatalf("evaluate path %q: %v", value, err)
	}
	return evaluated
}

func listIntegrationEvents(t *testing.T, ctx context.Context, dbStore *store.Store, sessionID string) []store.Event {
	t.Helper()

	events, err := dbStore.ListEvents(ctx, sessionID, 0, 100)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}

	return events
}

func assertEventTypes(t *testing.T, events []store.Event, want []string) {
	t.Helper()

	if len(events) != len(want) {
		t.Fatalf("expected %d events, got %d: %#v", len(want), len(events), events)
	}
	for i, event := range events {
		if event.Type != want[i] {
			t.Fatalf("expected event %d type %q, got %q", i, want[i], event.Type)
		}
	}
}

func hasEventType(t *testing.T, ctx context.Context, dbStore *store.Store, sessionID string, eventType string) bool {
	t.Helper()

	events := listIntegrationEvents(t, ctx, dbStore, sessionID)
	return hasEvent(events, eventType)
}

func hasEvent(events []store.Event, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func countEvents(events []store.Event, eventType string) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func hasQueuedUserMessage(events []store.Event) bool {
	for _, event := range events {
		if event.Type != "user.message.completed" {
			continue
		}
		payload := decodeEventPayloadNoTest(event)
		if _, ok := payload["queue_item_id"].(string); ok {
			return true
		}
	}
	return false
}

func decodeEventPayloadNoTest(event store.Event) map[string]any {
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return nil
	}
	return payload
}

func assertTerminalRunEventCount(t *testing.T, events []store.Event, want int) {
	t.Helper()

	count := 0
	for _, event := range events {
		if isTerminalRunEvent(event.Type) {
			count++
		}
	}
	if count != want {
		t.Fatalf("expected %d terminal run events, got %d in %#v", want, count, events)
	}
}

func assertPayloadText(t *testing.T, event store.Event, want string) {
	t.Helper()

	payload := decodeEventPayload(t, event)
	if payload["text"] != want {
		t.Fatalf("expected payload text %q, got %#v", want, payload["text"])
	}
}

func assertPayloadStatus(t *testing.T, event store.Event, want store.SessionStatus) {
	t.Helper()

	payload := decodeEventPayload(t, event)
	if payload["status"] != string(want) {
		t.Fatalf("expected payload status %q, got %#v", want, payload["status"])
	}
}

func assertPayloadAction(t *testing.T, event store.Event, want string) {
	t.Helper()

	payload := decodeEventPayload(t, event)
	if payload["action"] != want {
		t.Fatalf("expected payload action %q, got %#v", want, payload["action"])
	}
}

func assertMetadataValue(t *testing.T, metadata map[string]any, key string, want any) {
	t.Helper()

	if got := metadata[key]; got != want {
		t.Fatalf("expected metadata %s %#v, got %#v", key, want, got)
	}
}

func assertPayloadError(t *testing.T, event store.Event, want string) {
	t.Helper()

	payload := decodeEventPayload(t, event)
	if payload["error"] != want {
		t.Fatalf("expected payload error %q, got %#v", want, payload["error"])
	}
}

func decodeEventPayload(t *testing.T, event store.Event) map[string]any {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("decode event payload: %v", err)
	}

	return payload
}

type blockingAgent struct {
	agentType string
	started   chan agents.AgentInput
	releasec  chan struct{}
	once      sync.Once
}

type skillBlockingAgent struct {
	*blockingAgent
	catalog agents.SkillCatalog
	queries chan agents.SkillQuery
}

func newSkillBlockingAgent(catalog agents.SkillCatalog) *skillBlockingAgent {
	return &skillBlockingAgent{
		blockingAgent: newBlockingAgent(),
		catalog:       catalog,
		queries:       make(chan agents.SkillQuery, 8),
	}
}

func (a *skillBlockingAgent) Skills(_ context.Context, query agents.SkillQuery) (agents.SkillCatalog, error) {
	a.queries <- query
	return a.catalog, nil
}

func newBlockingAgent() *blockingAgent {
	return &blockingAgent{
		agentType: "fake",
		started:   make(chan agents.AgentInput, 1),
		releasec:  make(chan struct{}),
	}
}

func (a *blockingAgent) Type() string {
	return a.agentType
}

func (a *blockingAgent) Run(ctx context.Context, input agents.AgentInput, emit agents.EmitFunc) error {
	a.started <- input
	select {
	case <-a.releasec:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *blockingAgent) release() {
	a.once.Do(func() {
		close(a.releasec)
	})
}

type failingBlockingAgent struct {
	agentType string
	err       error
	started   chan agents.AgentInput
	releasec  chan struct{}
	once      sync.Once
}

func newFailingBlockingAgent(err error) *failingBlockingAgent {
	return &failingBlockingAgent{
		agentType: "fake",
		err:       err,
		started:   make(chan agents.AgentInput, 1),
		releasec:  make(chan struct{}),
	}
}

func (a *failingBlockingAgent) Type() string {
	return a.agentType
}

func (a *failingBlockingAgent) Run(ctx context.Context, input agents.AgentInput, emit agents.EmitFunc) error {
	a.started <- input
	if err := emit(ctx, agents.AgentEvent{
		Type:   "agent.run.started",
		Role:   "assistant",
		Status: string(store.EventStatusStarted),
		Payload: map[string]any{
			"agent_type": a.agentType,
		},
	}); err != nil {
		return err
	}
	select {
	case <-a.releasec:
		return a.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *failingBlockingAgent) release() {
	a.once.Do(func() {
		close(a.releasec)
	})
}

type userInputAgent struct{}

func (a userInputAgent) Type() string {
	return "fake"
}

type permissionAgent struct{}

func (permissionAgent) Type() string { return "fake" }
func (permissionAgent) Run(ctx context.Context, input agents.AgentInput, emit agents.EmitFunc) error {
	if err := emit(ctx, agents.AgentEvent{Type: "agent.run.started", Role: "assistant", Status: string(store.EventStatusStarted)}); err != nil {
		return err
	}
	request := agents.PermissionRequest{SessionID: input.SessionID, RequestID: "perm_test", Provider: "fake", ProviderEventType: "permission", Kind: "command", Title: "Approve command", Command: "git push", Options: []agents.PermissionOption{{ID: "allow-session", Label: "Allow for session", Decision: "allow", Scope: "session"}, {ID: "deny", Label: "Deny", Decision: "deny", Scope: "once"}}}
	waiter, err := input.Permissions.OpenPermission(ctx, request)
	if err != nil {
		return err
	}
	defer waiter.Close()
	if err := emit(ctx, agents.AgentEvent{Type: "agent.permission.requested", Role: "assistant", Status: string(store.EventStatusStarted), Payload: request}); err != nil {
		return err
	}
	response, err := waiter.Wait(ctx)
	if err != nil {
		return err
	}
	return emit(ctx, agents.AgentEvent{Type: "agent.message.completed", Role: "assistant", Status: string(store.EventStatusCompleted), Payload: map[string]any{"text": "Decision: " + response.OptionID}})
}

func (a userInputAgent) Run(ctx context.Context, input agents.AgentInput, emit agents.EmitFunc) error {
	if err := emit(ctx, agents.AgentEvent{
		Type:   "agent.run.started",
		Role:   "assistant",
		Status: string(store.EventStatusStarted),
		Payload: map[string]any{
			"agent_type": "fake",
		},
	}); err != nil {
		return err
	}

	request := agents.UserInputRequest{
		SessionID:         input.SessionID,
		RequestID:         "call_test",
		Provider:          "fake",
		ProviderEventType: "item/tool/requestUserInput",
		ProviderRequestID: "99",
		ThreadID:          "thread_test",
		TurnID:            "turn_test",
		ItemID:            "call_test",
		Questions: []agents.UserInputQuestion{
			{
				ID:       "question_test",
				Header:   "Pick",
				Question: "Pick one",
				IsOther:  false,
				Options: []agents.UserInputOption{
					{Label: "Alpha", Description: "First"},
					{Label: "Beta", Description: "Second"},
					{Label: "Gamma", Description: "Third"},
				},
			},
		},
	}
	waiter, err := input.UserInput.OpenUserInput(ctx, request)
	if err != nil {
		return err
	}
	defer waiter.Close()

	if err := emit(ctx, agents.AgentEvent{
		Type:   "agent.input.requested",
		Role:   "assistant",
		Status: string(store.EventStatusStarted),
		Payload: map[string]any{
			"provider":            request.Provider,
			"provider_event_type": request.ProviderEventType,
			"provider_request_id": request.ProviderRequestID,
			"request_id":          request.RequestID,
			"thread_id":           request.ThreadID,
			"turn_id":             request.TurnID,
			"item_id":             request.ItemID,
			"questions":           request.Questions,
		},
	}); err != nil {
		return err
	}

	response, err := waiter.Wait(ctx)
	if err != nil {
		return err
	}
	answer := response.Answers["question_test"].Answers[0]
	return emit(ctx, agents.AgentEvent{
		Type:   "agent.message.completed",
		Role:   "assistant",
		Status: string(store.EventStatusCompleted),
		Payload: map[string]any{
			"text": "Answer: " + answer,
		},
	})
}

type codexThreadAgent struct {
	threadID string
}

func (a codexThreadAgent) Type() string {
	return "codex"
}

func (a codexThreadAgent) Run(ctx context.Context, input agents.AgentInput, emit agents.EmitFunc) error {
	return emit(ctx, agents.AgentEvent{
		Type:   "agent.run.started",
		Role:   "assistant",
		Status: string(store.EventStatusStarted),
		Payload: map[string]any{
			"provider":            "codex",
			"provider_event_type": "thread/start",
			"thread_id":           a.threadID,
		},
	})
}

type claudeSessionAgent struct {
	sessionID string
}

func (a claudeSessionAgent) Type() string {
	return "claude"
}

func (a claudeSessionAgent) Run(ctx context.Context, input agents.AgentInput, emit agents.EmitFunc) error {
	return emit(ctx, agents.AgentEvent{
		Type:   "agent.run.started",
		Role:   "assistant",
		Status: string(store.EventStatusStarted),
		Payload: map[string]any{
			"provider":            "claude",
			"provider_event_type": "system/init",
			"provider_session_id": a.sessionID,
		},
	})
}

type opencodeSessionAgent struct {
	sessionID string
}

func (a opencodeSessionAgent) Type() string {
	return "opencode"
}

func (a opencodeSessionAgent) Run(ctx context.Context, input agents.AgentInput, emit agents.EmitFunc) error {
	return emit(ctx, agents.AgentEvent{
		Type:   "agent.run.started",
		Role:   "assistant",
		Status: string(store.EventStatusStarted),
		Payload: map[string]any{
			"provider":            "opencode",
			"provider_event_type": "session/new",
			"provider_session_id": a.sessionID,
		},
	})
}

type piSessionAgent struct {
	sessionID string
}

func (a piSessionAgent) Type() string {
	return "pi"
}

func (a piSessionAgent) Run(ctx context.Context, input agents.AgentInput, emit agents.EmitFunc) error {
	return emit(ctx, agents.AgentEvent{
		Type:   "agent.run.started",
		Role:   "assistant",
		Status: string(store.EventStatusStarted),
		Payload: map[string]any{
			"provider":            "pi",
			"provider_event_type": "get_state",
			"provider_session_id": a.sessionID,
		},
	})
}

type availabilityAgent struct {
	agentType    string
	availableErr error
}

func (a availabilityAgent) Type() string {
	return a.agentType
}

func (a availabilityAgent) Available() error {
	return a.availableErr
}

func (a availabilityAgent) Run(context.Context, agents.AgentInput, agents.EmitFunc) error {
	return nil
}

type optionsAgent struct {
	agentType    string
	availableErr error
	options      agents.Options
	optionsErr   error
}

func (a optionsAgent) Type() string {
	return a.agentType
}

func (a optionsAgent) Available() error {
	return a.availableErr
}

func (a optionsAgent) Options(context.Context) (agents.Options, error) {
	return a.options, a.optionsErr
}

func (a optionsAgent) Run(context.Context, agents.AgentInput, agents.EmitFunc) error {
	return nil
}

func decodeTestAgentOptions(t *testing.T, raw json.RawMessage) map[string]map[string]any {
	t.Helper()
	options := map[string]map[string]any{}
	if len(raw) == 0 {
		return options
	}
	if err := json.Unmarshal(raw, &options); err != nil {
		t.Fatalf("decode agent options: %v", err)
	}
	return options
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		if _, lookErr := exec.LookPath("git"); lookErr != nil {
			t.Skip("git is not available")
		}
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(output))
	}
}
