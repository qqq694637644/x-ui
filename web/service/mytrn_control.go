package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"x-ui/database/model"
	"x-ui/logger"
)

// MyTRN control is ordinary HTTP. A reaches it through its existing CF/VLESS
// SOCKS5 proxy. It is neither an Xray inbound nor the VLESS reverse tunnel.
//
// The listener is separate from the panel HTTP port. A changed port can be
// prepared before closing the old listener. Changing only the bind address
// on the same port (127.0.0.1 -> 0.0.0.0) is saved for the next panel restart:
// Linux cannot bind those addresses simultaneously, and no downtime-free
// listener swap is required for this personal deployment.
var mytrnControl struct {
	sync.Mutex
	address      string
	listener     net.Listener
	server       *http.Server
	prepared     net.Listener
	preparedAddr string
}

func prepareMyTRNControlListener(cfg *model.MyTRN) error {
	mytrnControl.Lock()
	defer mytrnControl.Unlock()
	if mytrnControl.prepared != nil {
		mytrnControl.prepared.Close()
		mytrnControl.prepared = nil
	}
	mytrnControl.preparedAddr = ""
	if !cfg.Enable {
		return nil
	}
	addr := ControlAddress(cfg)
	if mytrnControl.listener != nil && mytrnControl.address == addr {
		return nil
	}
	if mytrnControl.listener != nil {
		_, oldPort, oldErr := net.SplitHostPort(mytrnControl.address)
		_, newPort, newErr := net.SplitHostPort(addr)
		if oldErr == nil && newErr == nil && oldPort == newPort {
			// Do not attempt to bind a wildcard address over an existing
			// loopback listener. The DB is updated; the UI tells the user
			// that the new address takes effect after restarting x-ui.
			mytrnControl.preparedAddr = addr
			return nil
		}
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("MyTRN HTTP 控制监听 %s 失败: %w", addr, err)
	}
	mytrnControl.prepared = listener
	mytrnControl.preparedAddr = addr
	return nil
}

func discardPreparedMyTRNControlListener() {
	mytrnControl.Lock()
	defer mytrnControl.Unlock()
	if mytrnControl.prepared != nil {
		mytrnControl.prepared.Close()
		mytrnControl.prepared = nil
	}
	mytrnControl.preparedAddr = ""
}

func applyMyTRNControlListener(cfg *model.MyTRN) {
	mytrnControl.Lock()
	defer mytrnControl.Unlock()
	addr := ""
	if cfg.Enable {
		addr = ControlAddress(cfg)
	}
	if mytrnControl.listener != nil && mytrnControl.address == addr {
		return
	}
	if cfg.Enable && mytrnControl.listener != nil &&
		mytrnControl.prepared == nil && mytrnControl.preparedAddr == addr {
		logger.Infof("MyTRN control listener change to %s saved; restart x-ui to apply", addr)
		return
	}
	if mytrnControl.server != nil {
		mytrnControl.server.Close()
	}
	mytrnControl.listener = nil
	mytrnControl.server = nil
	mytrnControl.address = ""
	if !cfg.Enable {
		return
	}
	listener := mytrnControl.prepared
	mytrnControl.prepared = nil
	mytrnControl.preparedAddr = ""
	if listener == nil {
		// Programmer error: prepare must have succeeded before apply.
		logger.Error("MyTRN HTTP listener missing after prepare")
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/control/mapping", handleMyTRNMapping)
	server := &http.Server{
		Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 12 * time.Second,
		MaxHeaderBytes: 4096,
	}
	mytrnControl.listener = listener
	mytrnControl.server = server
	mytrnControl.address = addr
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("MyTRN HTTP control listener failed:", err)
		}
	}()
	logger.Infof("MyTRN control HTTP listening on %s", addr)
}

func isMyTRNControlListenerRestartRequired(cfg *model.MyTRN) bool {
	mytrnControl.Lock()
	defer mytrnControl.Unlock()
	return cfg.Enable && mytrnControl.listener != nil && mytrnControl.address != ControlAddress(cfg)
}

func mytrnControlListenerMessage(cfg *model.MyTRN) string {
	mytrnControl.Lock()
	defer mytrnControl.Unlock()
	if !cfg.Enable {
		return ""
	}
	if mytrnControl.listener == nil {
		return "控制 HTTP 监听未运行；检查端口占用后重启 x-ui 面板"
	}
	if mytrnControl.address != ControlAddress(cfg) {
		return "控制监听地址变更已保存，重启 x-ui 面板后生效"
	}
	return ""
}

func StartMyTRNControl() error {
	cfg, err := (&MyTRNService{}).Get()
	if err != nil {
		return err
	}
	if cfg.Enable {
		if err := validateMyTRNSettings(cfg); err != nil {
			return err
		}
	}
	if err := prepareMyTRNControlListener(cfg); err != nil {
		return err
	}
	applyMyTRNControlListener(cfg)
	return nil
}

func StopMyTRNControl() {
	mytrnControl.Lock()
	defer mytrnControl.Unlock()
	if mytrnControl.server != nil {
		mytrnControl.server.Close()
		mytrnControl.server = nil
	}
	if mytrnControl.prepared != nil {
		mytrnControl.prepared.Close()
		mytrnControl.prepared = nil
	}
	mytrnControl.preparedAddr = ""
	mytrnControl.listener = nil
	mytrnControl.address = ""
}

func handleMyTRNMapping(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.URL.Path != "/control/mapping" || r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		http.Error(w, `{"error":"expected application/json"}`, http.StatusUnsupportedMediaType)
		return
	}
	// Authenticate BEFORE reading/parsing the supplied certificate or endpoint.
	cfg, err := (&MyTRNService{}).Get()
	if err != nil {
		http.Error(w, `{"error":"server error"}`, http.StatusInternalServerError)
		return
	}
	if !cfg.Enable || cfg.ControlToken == "" || !constantTimeTokenMatch(cfg.ControlToken, r.Header.Get("X-Control-Token")) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusForbidden)
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 12*1024))
	decoder.DisallowUnknownFields()
	var req MyTRNRegistration
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON request"}`, http.StatusBadRequest)
		return
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		http.Error(w, `{"error":"trailing JSON data"}`, http.StatusBadRequest)
		return
	}
	response, err := (&MyTRNService{}).Register(r.Header.Get("X-Control-Token"), req)
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, errMyTRNUnauthorized) {
			code = http.StatusForbidden
		} else if errors.Is(err, errMyTRNCertificateChanged) {
			code = http.StatusConflict
		} else if strings.Contains(err.Error(), "database") {
			code = http.StatusInternalServerError
		}
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok": true, "changed": response.Changed,
		"certificate_sha256": response.Fingerprint,
	})
}
