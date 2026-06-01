package hosting

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/dot-zypo/daemon/common/node"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

var (
	wasmRuntime wazero.Runtime
	wasmCache   sync.Map // string -> wazero.CompiledModule
	wasmOnce    sync.Once
)

func getWasmRuntime(ctx context.Context) wazero.Runtime {
	wasmOnce.Do(func() {
		wasmRuntime = wazero.NewRuntime(ctx)
		wasi_snapshot_preview1.MustInstantiate(ctx, wasmRuntime)
	})
	return wasmRuntime
}

func ExecuteWasm(n *node.ZypoNode, wasmPath string, req *http.Request) ([]byte, error) {
	ctx := context.Background()
	r := getWasmRuntime(ctx)

	var compiled wazero.CompiledModule
	if val, ok := wasmCache.Load(wasmPath); ok {
		compiled = val.(wazero.CompiledModule)
	} else {
		wasmBytes, err := os.ReadFile(wasmPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read wasm file: %w", err)
		}

		compiled, err = r.CompileModule(ctx, wasmBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to compile wasm: %w", err)
		}
		wasmCache.Store(wasmPath, compiled)
	}

	var stdinBytes []byte
	if req.Body != nil {
		buf := new(bytes.Buffer)
		buf.ReadFrom(req.Body)
		stdinBytes = buf.Bytes()
	}

	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer

	// Each instantiation needs a unique name if they coexist in the same runtime,
	// but here we close it immediately. We use WithName("") to let wazero
	// handle internal naming or use a unique suffix.
	config := wazero.NewModuleConfig().
		WithName("").
		WithStdin(bytes.NewReader(stdinBytes)).
		WithStdout(&stdoutBuf).
		WithStderr(&stderrBuf).
		WithEnv("REQUEST_METHOD", req.Method).
		WithEnv("PATH_INFO", req.URL.Path).
		WithEnv("QUERY_STRING", req.URL.RawQuery).
		WithEnv("HTTP_HOST", req.Host).
		WithArgs("zypo-wasm")

	for k, v := range req.Header {
		config = config.WithEnv("HTTP_"+k, v[0])
	}

	mod, err := r.InstantiateModule(ctx, compiled, config)
	if err != nil {
		log.Printf("WASM Stderr: %s", stderrBuf.String())
		return nil, fmt.Errorf("wasm execution failed: %w", err)
	}
	defer mod.Close(ctx)

	return stdoutBuf.Bytes(), nil
}
