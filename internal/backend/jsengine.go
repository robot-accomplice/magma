package backend

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/robot-accomplice/magma/internal/backend/jsdriver"
	"github.com/robot-accomplice/magma/internal/backend/jsvendor"
)

// nodeEnv overrides the node executable. Checked before PATH so a developer can
// point at a specific runtime without changing their environment.
const nodeEnv = "MAGMA_JS_HELPER_NODE"

// jsHelperContractVersion is the only driver wire format this backend reads.
const jsHelperContractVersion = "magma-js-helper/1"

// jsEngine is whatever actually analyses the repository.
//
// An interface rather than a direct call into the driver, because the engine is
// expected to change. TypeScript 7 is written in Go, and if it ever exposes a
// public API magma could link it in-process and drop BOTH the node requirement
// and the vendored compiler. That must be a new implementation of this
// interface, not a rewrite of the backend — the same "a new thing is an
// addition, not an edit" discipline the rust-helper refactor established.
type jsEngine interface {
	Analyse(repo string, step func(string)) (jsEnvelope, error)
}

type jsEnvelope struct {
	ContractVersion     string       `json:"contract_version"`
	Computable          *bool        `json:"computable"`
	Reason              string       `json:"reason"`
	ExecutedTargetCode  bool         `json:"executed_target_code"`
	UnresolvedCallSites int          `json:"unresolved_call_sites"`
	Functions           []jsFunction `json:"functions"`
	Calls               []jsCall     `json:"calls"`
}

type jsFunction struct {
	ID        int    `json:"id"`
	Symbol    string `json:"symbol"`
	Pkg       string `json:"pkg"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Kind      string `json:"kind"`
	Exported  bool   `json:"exported"`
	Test      bool   `json:"test"`
	Root      bool   `json:"root"`
	Generated bool   `json:"generated"`
}

type jsCall struct {
	From     int    `json:"from"`
	To       int    `json:"to"`
	SiteFile string `json:"site_file"`
	SiteLine int    `json:"site_line"`
	Kind     string `json:"kind"`
}

// nodeEngine runs the embedded driver under the target's own node.
type nodeEngine struct{}

func (nodeEngine) Analyse(repo string, step func(string)) (jsEnvelope, error) {
	bin, err := findNode()
	if err != nil {
		return jsEnvelope{}, err
	}
	cache, err := helperCacheDir()
	if err != nil {
		return jsEnvelope{}, err
	}
	step("preparing analyser")
	if err := jsvendor.Extract(cache); err != nil {
		return jsEnvelope{}, fmt.Errorf("extracting the vendored compiler: %w", err)
	}
	if err := jsdriver.Extract(cache); err != nil {
		return jsEnvelope{}, fmt.Errorf("extracting the driver: %w", err)
	}

	step("analyzing workspace")
	// stderr is both forwarded as live progress and buffered: it is the only
	// diagnostic available if the driver fails.
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, filepath.Join(cache, "driver.js"), repo)
	cmd.Stdout = &stdout
	cmd.Stderr = io.MultiWriter(&stderr, &progressLines{fn: step})
	if err := cmd.Run(); err != nil {
		// The driver reserves nonzero exits for usage misuse; a data-driven
		// refusal exits 0 WITH an envelope. So a nonzero exit here is a genuine
		// failure to run, not an answer.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return jsEnvelope{}, fmt.Errorf("js driver exited %d: %s", ee.ExitCode(), stderr.String())
		}
		return jsEnvelope{}, fmt.Errorf("running the js driver: %w", err)
	}
	var env jsEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		return jsEnvelope{}, fmt.Errorf("parsing js driver output: %w", err)
	}
	if env.ContractVersion != jsHelperContractVersion {
		return jsEnvelope{}, fmt.Errorf(
			"js driver speaks %q, this magma reads %q", env.ContractVersion, jsHelperContractVersion)
	}
	return env, nil
}

// helperCacheDir is where the compiler and driver are extracted. Under the user
// cache dir and keyed by vendored version, so the 12.5 MB extraction is paid
// once per magma version rather than on every run, and a version bump cannot
// silently reuse the previous compiler.
func helperCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "magma", "js", jsvendor.Version), nil
}

// findNode resolves the runtime, preferring an explicit override over PATH. The
// error text is user-facing: it becomes the refusal reason verbatim, so it says
// what is missing AND how to fix it, attributed per the limitations vocabulary.
func findNode() (string, error) {
	if p := os.Getenv(nodeEnv); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("%s=%q is not usable: %v", nodeEnv, p, err)
		}
		return p, nil
	}
	p, err := exec.LookPath("node")
	if err != nil {
		return "", fmt.Errorf(
			"not supported by magma's node backend: node not found on PATH or $%s", nodeEnv)
	}
	return p, nil
}
