package dredd_test

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/ondbyte/dredd/dreddtest"
	"github.com/ondbyte/dredd/langs"
)

// TestEndToEnd_AllLanguagesDocker is the non-negotiable variant of the
// language matrix test: every entry must run via a real Docker container
// using its declared Image, and every entry must produce the expected
// "hello-<id>\n" stdout. Languages are NOT skipped — if a required image
// can't be pulled / built, the whole test fails.
//
// Gated behind DREDD_DOCKER_LANG_TEST=1 because (a) it requires Docker, and
// (b) it pulls/builds many images (~10 GB) on first run.
//
// First time setup (one shot, several minutes):
//
//	./dreddtest/images/build.sh        # build the custom images
//	DREDD_DOCKER_LANG_TEST=1 go test -run AllLanguagesDocker ./... -timeout 60m
//
// Override which executor binary is used (`docker`, `podman`, `nerdctl`)
// with DREDD_DOCKER_BIN.
func TestEndToEnd_AllLanguagesDocker(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped under -short")
	}
	if v := envOr("DREDD_DOCKER_LANG_TEST", ""); v == "" {
		t.Skip("set DREDD_DOCKER_LANG_TEST=1 to run the Docker-backed all-languages test")
	}

	dockerBin := envOr("DREDD_DOCKER_BIN", "docker")
	if _, err := exec.LookPath(dockerBin); err != nil {
		t.Fatalf("docker binary %q not on PATH: %v", dockerBin, err)
	}
	// Verify the daemon is reachable. We fail (not skip) because this test
	// is meant to be the definitive verification.
	infoCtx, infoCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer infoCancel()
	if err := exec.CommandContext(infoCtx, dockerBin, "info").Run(); err != nil {
		t.Fatalf("docker daemon not reachable: %v", err)
	}

	cases := languageCases()

	// Build the image map and the dredd language catalogue.
	images := make(map[string]string, len(cases))
	catalogue := make([]langs.Language, 0, len(cases))
	for _, c := range cases {
		if c.Image == "" {
			t.Fatalf("langCase %q has no Image set", c.ID)
		}
		images[c.ID] = c.Image
		catalogue = append(catalogue, c.dockerLanguage())
	}

	exe := dreddtest.NewDockerExecutor(images)
	exe.DockerBin = dockerBin
	// A few languages need to install something at compile time
	// (TypeScript via npm). Override the default --network=none for those
	// images so npm can reach the registry.
	exe.PerImageNetwork = map[string]string{
		"node:20": "bridge",
	}

	// Pre-pull every image up front so the per-case timings only reflect
	// compile+run work, and so missing images surface immediately. Gated
	// on DREDD_DOCKER_PREWARM=1 so single-subtest runs (-run /bash) don't
	// pay the cost of pulling every image. The full `make test-languages`
	// flow enables prewarm.
	if envOr("DREDD_DOCKER_PREWARM", "") != "" {
		pullCtx, pullCancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer pullCancel()
		if err := exe.PrewarmImages(pullCtx); err != nil {
			t.Fatalf("pre-pull images: %v", err)
		}
	}

	i := startInfraWithLanguages(t, exe, catalogue)

	var (
		passed []string
		failed []string
	)

	for _, c := range cases {
		ran := false
		ok := t.Run(c.ID, func(t *testing.T) {
			ran = true
			id := submit(t, i, map[string]any{
				"language":      c.ID,
				"source":        c.Source,
				"stdins":        []string{""},
				"time_limit_ms": 180000, // 3 min — JVM / scala first-runs are slow
			})
			status := waitForStatus(t, i, id, "done", 4*time.Minute)
			results, _ := status["results"].([]any)
			if len(results) != 1 {
				t.Fatalf("want 1 result, got %d: %+v", len(results), status)
			}
			r := results[0].(map[string]any)
			got, _ := r["stdout"].(string)
			if got != c.expected() {
				t.Errorf("stdout = %q, want %q (stderr=%q, compile_error=%q, exit=%v)",
					got, c.expected(), r["stderr"], status["compile_error"], r["exit_code"])
			}
		})
		if !ran {
			continue // filtered out by -run
		}
		if ok {
			passed = append(passed, c.ID)
		} else {
			failed = append(failed, c.ID)
		}
	}

	// Final summary so failures aren't lost under verbose docker logs.
	t.Logf("AllLanguagesDocker matrix: %d/%d passed", len(passed), len(passed)+len(failed))
	if len(failed) > 0 {
		t.Logf("FAILED: %v", failed)
	}
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
