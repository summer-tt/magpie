package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func automaticAliasFixture(t *testing.T, reply string) (*Server, func() []routingAliasCall) {
	t.Helper()
	fresh(t)
	var mu sync.Mutex
	var calls []routingAliasCall
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[{"id":"m"}]}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, routingAliasCall{strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), modelOf(b)})
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, predictionReply(reply))
	}))
	t.Cleanup(up.Close)
	for _, p := range []provider.Provider{{ID: "automatic-bound", Name: "Bound fixture", Chat: up.URL + "/v1", Key: "fixture-bound", Models: []string{"m"}}, {ID: "automatic-other", Name: "Other fixture", Chat: up.URL + "/v1", Key: "fixture-other", Models: []string{"m"}}} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	take := func() []routingAliasCall {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(calls)
		calls = nil
		return out
	}
	s := New()
	if w := codexAsks(s, "automatic-bound/m", "", workerAsk); w.Code != 200 {
		t.Fatalf("baseline ordinary backend: %d %s", w.Code, w.Body.String())
	}
	if got := take(); !slices.Equal(got, []routingAliasCall{{"fixture-bound", "m"}}) {
		t.Fatalf("baseline ordinary owner/plain model=%v", got)
	}
	if err := provider.SetKeyRoutingAlias("automatic-bound", provider.KeyID("fixture-bound"), "work"); err != nil {
		t.Fatal(err)
	}
	return s, take
}

func TestRoutingAliasAutomaticSideCallsStillSucceedOnOriginalBinding(t *testing.T) {
	for _, tc := range []struct{ kind, schema, reply string }{
		{"thread_title", sideTitleSchema, `{"title":"Fixture title","description":"Fixture description"}`},
		{"thread_description", sideDescriptionSchema, `{"description":"Fixture description"}`},
		{"composer_predictions", predictionSchema, `{"suggestion":"Run the tests"}`},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			s, take := automaticAliasFixture(t, tc.reply)
			send := func(ref string) *httptest.ResponseRecorder {
				if tc.kind == "composer_predictions" {
					return codexPredictionRequest(t, s, CodexPath+"/responses", ref)
				}
				body := `{"model":"` + ref + `","stream":true,"input":[` + workerAsk + `],"text":{"format":{"type":"json_schema","name":"codex_output_schema","strict":true,"schema":` + tc.schema + `}}}`
				r := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer chatgpt-token")
				r.Header.Set("User-Agent", "codex_cli_rs/0.162.1 (Linux 6.8; x86_64)")
				r.Header.Set("session_id", "automatic-side-thread")
				r.Header.Set("x-codex-turn-metadata", `{"thread_source":"`+tc.kind+`"}`)
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				return w
			}
			const ref = "automatic-bound/m@work"
			baseline := send(ref)
			if baseline.Code != 200 {
				t.Fatalf("baseline valid qualified %s: %d %s", tc.kind, baseline.Code, baseline.Body.String())
			}
			if text, _ := outputText(t, baseline.Body.String()); text == "" {
				t.Fatal("baseline side response contains no completed text")
			}
			if got := take(); !slices.Equal(got, []routingAliasCall{{"fixture-bound", "m"}}) {
				t.Fatalf("baseline side binding=%v", got)
			}
			st := settings.Load()
			st.CodexTitles = "automatic-other/m"
			st.CodexDescriptions = "automatic-other/m"
			st.CodexPredictions = "automatic-other/m"
			if err := settings.Save(st); err != nil {
				t.Fatal(err)
			}
			w := send(ref)
			got := take()
			if w.Code != 200 {
				t.Errorf("legal qualified %s failed after automatic model setting: %d %s", tc.kind, w.Code, w.Body.String())
			} else if text, _ := outputText(t, w.Body.String()); text == "" {
				t.Error("qualified automatic side request produced no completed output")
			}
			if !slices.Equal(got, []routingAliasCall{{"fixture-bound", "m"}}) {
				t.Errorf("automatic %s did not succeed on original owner/plain model: %v", tc.kind, got)
			}
			w = send("automatic-bound/m@unknown")
			if w.Code != 404 {
				t.Errorf("automatic %s unknown alias status=%d", tc.kind, w.Code)
			}
			if got := take(); len(got) != 0 {
				t.Errorf("automatic %s unknown alias consumed upstream=%v", tc.kind, got)
			}
		})
	}
}

func TestRoutingAliasClaudeClassifierStillSucceedsOnOriginalBinding(t *testing.T) {
	s, take := automaticAliasFixture(t, "<block>no")
	send := func(ref string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(autoModeAsk(ref)))
		r.Header.Set("User-Agent", "claude-cli/2.1.310 (external, cli)")
		r.Header.Set("x-api-key", Token+"-claude")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	const ref = "automatic-bound/m@work"
	if w := send(ref); w.Code != 200 || !saidNo(w.Body.String()) {
		t.Fatalf("baseline real auto-mode classifier: %d %s", w.Code, w.Body.String())
	}
	if got := take(); !slices.Equal(got, []routingAliasCall{{"fixture-bound", "m"}}) {
		t.Fatalf("baseline classifier binding=%v", got)
	}
	old := ClassifierStandIn
	t.Cleanup(func() { ClassifierStandIn = old })
	hookCalls := 0
	ClassifierStandIn = func(agent, model string) string {
		if agent == "claude" {
			hookCalls++
			return "automatic-other/m"
		}
		return ""
	}
	if w := send("automatic-other/m"); w.Code != 200 || !saidNo(w.Body.String()) {
		t.Fatalf("baseline classifier hook target: %d %s", w.Code, w.Body.String())
	}
	if got := take(); !slices.Equal(got, []routingAliasCall{{"fixture-other", "m"}}) || hookCalls == 0 {
		t.Fatalf("classifier hook/target fixture not reached: calls=%v hookCalls=%d", got, hookCalls)
	}
	w := send(ref)
	got := take()
	if w.Code != 200 || !saidNo(w.Body.String()) {
		t.Errorf("qualified classifier failed after stand-in setting: %d %s", w.Code, w.Body.String())
	}
	if !slices.Equal(got, []routingAliasCall{{"fixture-bound", "m"}}) {
		t.Errorf("classifier changed original owner/plain model=%v", got)
	}
	if err := provider.SetKeyRoutingAlias("automatic-bound", provider.KeyID("fixture-bound"), ""); err != nil {
		t.Fatal(err)
	}
	w = send(ref)
	if w.Code != 404 {
		t.Errorf("retired classifier alias status=%d", w.Code)
	}
	if got := take(); len(got) != 0 {
		t.Errorf("retired classifier alias consumed stand-in=%v", got)
	}
}

func TestRoutingAliasSubagentAndResumeStillSucceedOnOriginalBinding(t *testing.T) {
	for _, mode := range []string{"configured-subagent", "remembered-resume"} {
		t.Run(mode, func(t *testing.T) {
			s, take := automaticAliasFixture(t, "on it")
			const ref = "automatic-bound/m@work"
			if w := codexAsks(s, ref, "collab_spawn", plainTask); w.Code != 200 {
				t.Fatalf("baseline qualified collab_spawn: %d %s", w.Code, w.Body.String())
			}
			if got := take(); !slices.Equal(got, []routingAliasCall{{"fixture-bound", "m"}}) {
				t.Fatalf("baseline collab_spawn owner/plain model=%v", got)
			}
			if mode == "remembered-resume" {
				if w := codexThreadAsks(s, "automatic-other/m", "binding-worker", "binding-lead"); w.Code != 200 {
					t.Fatalf("baseline prior thread: %d %s", w.Code, w.Body.String())
				}
				if got := take(); !slices.Equal(got, []routingAliasCall{{"fixture-other", "m"}}) {
					t.Fatalf("baseline prior thread owner=%v", got)
				}
			}
			oldStandIn := StandIn
			t.Cleanup(func() { StandIn = oldStandIn })
			StandIn = func(string, string) string { return "automatic-other/m" }
			// Prove the configured stand-in really serves normal unknown requests.
			r := httptest.NewRequest("POST", "http://localhost/v1/chat/completions", strings.NewReader(`{"model":"fixture-unlisted-native","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
			r.RemoteAddr = "127.0.0.1:1234"
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("baseline stand-in: %d %s", w.Code, w.Body.String())
			}
			if got := take(); !slices.Equal(got, []routingAliasCall{{"fixture-other", "m"}}) {
				t.Fatalf("baseline stand-in target=%v", got)
			}
			if err := provider.SetCodexSubagentModel("automatic-other/m"); err != nil {
				t.Fatal(err)
			}
			send := func(model string) *httptest.ResponseRecorder {
				if mode == "remembered-resume" {
					return codexThreadAsks(s, model, "binding-worker", "binding-lead")
				}
				return codexAsks(s, model, "collab_spawn", plainTask)
			}
			w = send(ref)
			got := take()
			if w.Code != 200 {
				t.Errorf("legal qualified %s failed after automatic settings: %d %s", mode, w.Code, w.Body.String())
			}
			if !slices.Equal(got, []routingAliasCall{{"fixture-bound", "m"}}) {
				t.Errorf("%s changed original owner/plain model=%v", mode, got)
			}
			if err := provider.SetKeyRoutingAlias("automatic-bound", provider.KeyID("fixture-bound"), ""); err != nil {
				t.Fatal(err)
			}
			w = send(ref)
			if w.Code != 404 {
				t.Errorf("retired %s alias status=%d", mode, w.Code)
			}
			if got := take(); len(got) != 0 {
				t.Errorf("retired %s alias consumed automatic replacement=%v", mode, got)
			}
		})
	}
}
