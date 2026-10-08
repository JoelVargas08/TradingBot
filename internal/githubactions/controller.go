package githubactions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Controller permite que el propio runner cancele su ejecución de GitHub Actions.
// No abre operaciones reales: solo controla el ciclo de vida del workflow PAPER.
type Controller struct {
	token      string
	repository string
	runID      string
	workflow   string
	ref        string
	client     *http.Client
}

func NewFromEnv() *Controller {
	return &Controller{
		token:      strings.TrimSpace(os.Getenv("GITHUB_TOKEN")),
		repository: strings.TrimSpace(os.Getenv("GITHUB_REPOSITORY")),
		runID:      strings.TrimSpace(os.Getenv("GITHUB_RUN_ID")),
		workflow:   "paper-session.yml",
		ref:        "main",
		client:     &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Controller) Available() bool {
	return c != nil && c.token != "" && c.repository != "" && c.runID != ""
}

func (c *Controller) RunID() string {
	if c == nil {
		return ""
	}
	return c.runID
}

// CancelCurrent cancela el workflow que está ejecutando al bot.
func (c *Controller) CancelCurrent(ctx context.Context) error {
	if !c.Available() {
		return fmt.Errorf("control de GitHub Actions no disponible: GITHUB_TOKEN/GITHUB_REPOSITORY/GITHUB_RUN_ID incompletos")
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/actions/runs/%s/cancel", c.repository, c.runID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte{}))
	if err != nil {
		return err
	}
	c.setHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("GitHub cancel workflow: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *Controller) setHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
}

type WorkflowDispatch struct {
	Ref    string            `json:"ref"`
	Inputs map[string]string `json:"inputs,omitempty"`
}

// DispatchPaper inicia una nueva sesión PAPER desde el branch principal.
func (c *Controller) DispatchPaper(ctx context.Context, strategyID string, restoreState bool) error {
	if c == nil || c.token == "" || c.repository == "" {
		return fmt.Errorf("control de GitHub Actions no disponible")
	}
	ref := c.ref
	if ref == "" {
		ref = "main"
	}
	inputs := map[string]string{
		"restore_state": strconv.FormatBool(restoreState),
	}
	if strings.TrimSpace(strategyID) != "" {
		inputs["strategy_id"] = strings.TrimSpace(strategyID)
	}
	payload, err := json.Marshal(WorkflowDispatch{Ref: ref, Inputs: inputs})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/actions/workflows/%s/dispatches", c.repository, c.workflow)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("GitHub dispatch workflow: HTTP %d", resp.StatusCode)
	}
	return nil
}
