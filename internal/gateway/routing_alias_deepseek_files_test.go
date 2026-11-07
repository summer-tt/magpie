package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func aliasFilesProvider(t *testing.T, address, mode string, rpm int) provider.Provider {
	t.Helper()
	p := provider.Provider{ID: "alias-files", Name: "Files", Chat: address + "/v1", Key: "fixture-files-key", Models: []string{"deepseek-flash"}, MaxRPM: rpm}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	ref := p.ID + "/deepseek-flash"
	if mode == "qualified-retired" {
		if err := provider.SetKeyRoutingAlias(p.ID, provider.KeyID(p.Key), "work"); err != nil {
			t.Fatal(err)
		}
		ref += "@work"
	}
	p, _, err := provider.ResolveModel(ref)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The first four uploads are already sent and may finish. A fifth upload
// waiting for their slot must use the binding current when it is sent.
func TestRoutingAliasDeepSeekUploadSlot(t *testing.T) {
	for _, mode := range []string{"ordinary-isolated", "qualified-retired"} {
		for _, changed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/changed=%t", mode, changed), func(t *testing.T) {
				fresh(t)
				entered := make(chan struct{}, dsFilesAtOnce+1)
				release := make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				var calls atomic.Int32
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/files" || r.Header.Get("Authorization") != "Bearer fixture-files-key" {
						t.Error("upload did not use its selected endpoint and key")
					}
					n := calls.Add(1)
					entered <- struct{}{}
					<-release
					fmt.Fprintf(w, `{"id":"file-api-%d"}`, n)
				}))
				t.Cleanup(up.Close)
				p := aliasFilesProvider(t, up.URL, mode, 0)
				s := New()
				images := make([]dsImage, dsFilesAtOnce+1)
				for i := range images {
					images[i] = dsImage{key: fmt.Sprintf("fixture:%d", i), mime: "image/png", b64: dsPNG1}
				}
				done := make(chan map[string]string, 1)
				go func() { done <- s.dsResolve(t.Context(), p, provider.Chat, "fixture", images) }()
				finished := false
				t.Cleanup(func() {
					unblock()
					if !finished {
						<-done
					}
				})
				for range dsFilesAtOnce {
					select {
					case <-entered:
					case <-time.After(10 * time.Second):
						t.Fatal("legal uploads did not fill the upload slots")
					}
				}
				if changed {
					aliasRetryChangeBinding(t, p.ID, p.Key, mode)
				}
				unblock()
				got := <-done
				finished = true
				want := dsFilesAtOnce + 1
				if changed {
					want--
				}
				if len(got) != want || int(calls.Load()) != want {
					t.Fatalf("uploads reaching vendor=%d, file references=%d, want %d", calls.Load(), len(got), want)
				}
			})
		}
	}
}

func TestRoutingAliasDeepSeekUploadRPM(t *testing.T) {
	for _, mode := range []string{"ordinary-isolated", "qualified-retired"} {
		for _, changed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/changed=%t", mode, changed), func(t *testing.T) {
				fresh(t)
				clock := holdMinute(t)
				waiting := make(chan struct{}, 1)
				previous := rpmTimer
				rpmTimer = func(d time.Duration) (<-chan time.Time, func() bool) {
					ch, stop := previous(d)
					select {
					case waiting <- struct{}{}:
					default:
					}
					return ch, stop
				}
				t.Cleanup(func() { rpmTimer = previous })
				var calls atomic.Int32
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					io.WriteString(w, `{"id":"file-api-ok"}`)
				}))
				t.Cleanup(up.Close)
				p := aliasFilesProvider(t, up.URL, mode, 1)
				s := New()
				im := dsImage{mime: "image/png", b64: dsPNG1}
				send := func(ctx context.Context) error {
					_, err := s.dsUpload(s.metered(ctx, p, ""), p, provider.Chat, im)
					return err
				}
				if err := send(t.Context()); err != nil {
					t.Fatalf("legal upload: %v", err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				done := make(chan error, 1)
				go func() { done <- send(ctx) }()
				finished := false
				t.Cleanup(func() {
					cancel()
					clock.release()
					if !finished {
						<-done
					}
				})
				select {
				case <-waiting:
				case err := <-done:
					finished = true
					t.Fatalf("upload ended before RPM wait: %v", err)
				case <-ctx.Done():
					t.Fatal("upload did not enter RPM wait")
				}
				if changed {
					aliasRetryChangeBinding(t, p.ID, p.Key, mode)
				}
				clock.advance(rpmSpan())
				err := <-done
				finished = true
				if ctx.Err() != nil {
					t.Fatal("cancellation is not a routing refusal")
				}
				want := int32(2)
				if changed {
					want = 1
				}
				if (err != nil) != changed || calls.Load() != want {
					t.Fatalf("upload error=%v, vendor calls=%d, want changed=%t calls=%d", err, calls.Load(), changed, want)
				}
			})
		}
	}
}
