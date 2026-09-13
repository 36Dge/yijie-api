package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	wire "github.com/36Dge/yijie-api/internal/contracts/workflowlocal"
	"github.com/36Dge/yijie-api/internal/modules/workflows/domain"
	"github.com/36Dge/yijie-api/internal/modules/workflows/transport"
	"github.com/36Dge/yijie-api/internal/workflowlocal"
)

// checkReady performs one authenticated local read. It does not open a database,
// apply migrations, bind a principal, start a listener or accept a URL argument.
func checkReady() error {
	unavailable := errors.New("workflow readiness unavailable")
	config, err := workflowlocal.LoadConfig()
	if err != nil {
		return unavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:18888/v1/workflow-local/status", nil)
	if err != nil {
		return unavailable
	}
	request.Header.Set("Authorization", "Bearer "+config.Native.Token)
	request.Header.Set("X-Yijie-Run-Epoch", config.Native.RunEpoch)
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	client := &http.Client{Transport: t, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return unavailable
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, domain.MessageBytes+1))
	if err != nil || len(data) > domain.MessageBytes || response.StatusCode != 200 || transport.Validate("ServiceStatus", data) != nil {
		return unavailable
	}
	var status wire.ServiceStatus
	if json.Unmarshal(data, &status) != nil || !status.Ready || status.State != wire.Ready || status.ProtocolVersion != 1 || status.RunEpoch != config.Native.RunEpoch {
		return unavailable
	}
	return nil
}
