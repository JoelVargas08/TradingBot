package broker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Constantes de la API de contratos WEEX, aisladas en UN único sitio para que
// cualquier ajuste a la doc real se haga solo aquí.
const (
	// Cabeceras de autenticación.
	headerAccessKey   = "X-ACCESS-KEY"
	headerAccessSign  = "X-ACCESS-SIGN"
	headerAccessTS    = "X-ACCESS-TIMESTAMP"
	weexContractBase  = "https://api-contract.weex.com"
	weexSuccessCode   = "0"
)

// Endpoints REST de contratos WEEX (v2).
const (
	pathOrderCreate   = "/v2/order/create"
	pathOrderCancel   = "/v2/order/cancel"
	pathOrderList     = "/v2/order/list"
	pathPositionList  = "/v2/position/list"
	pathAccountList   = "/v2/account/list"
)

// WeexConfig configura el acceso autenticado a la API de contratos WEEX.
type WeexConfig struct {
	BaseURL    string
	APIKey     string
	APISecret  string
	HTTPClient *http.Client
	Testnet    bool
}

// WeexClient es el cliente REST firmado de contratos WEEX. Nunca imprime el
// secret ni las cabeceras de autenticación.
type WeexClient struct {
	cfg    WeexConfig
	client *http.Client
	now    func() time.Time
}

func NewWeexClient(cfg WeexConfig) *WeexClient {
	if cfg.BaseURL == "" {
		cfg.BaseURL = weexContractBase
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &WeexClient{cfg: cfg, client: cfg.HTTPClient, now: time.Now}
}

// sign firma la petición con HMAC-SHA256(secret) sobre:
//
//	timestamp + "&" + query(ordenada y url-encoded) + "&" + body crudo
//
// La cabecera X-ACCESS-TIMESTAMP se genera en milisegundos Unix; si la doc
// real de WEEX usara segundos o un orden distinto de la payload, basta con
// ajustar este único método.
func (c *WeexClient) sign(timestamp string, query string, body []byte) string {
	msg := timestamp
	if query != "" {
		msg += "&" + query
	}
	if len(body) > 0 {
		msg += "&" + string(body)
	}
	mac := hmac.New(sha256.New, []byte(c.cfg.APISecret))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}

// doAuth ejecuta una petición autenticada y decodifica la respuesta WEEX en
// out. El body (si no es nil) se firma junto a la query y el timestamp.
func (c *WeexClient) doAuth(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	if c.cfg.APIKey == "" || c.cfg.APISecret == "" {
		return fmt.Errorf("weex: API key/secret requeridos")
	}
	var rawQuery string
	if len(query) > 0 {
		keys := make([]string, 0, len(query))
		for k := range query {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			for _, v := range query[k] {
				parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
			}
		}
		rawQuery = strings.Join(parts, "&")
	}
	var bodyBytes []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("weex: marshal body: %w", err)
		}
		bodyBytes = b
	}

	ts := fmt.Sprintf("%d", c.now().UnixMilli())
	signature := c.sign(ts, rawQuery, bodyBytes)

	target := strings.TrimRight(c.cfg.BaseURL, "/") + path
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set(headerAccessKey, c.cfg.APIKey)
	req.Header.Set(headerAccessSign, signature)
	req.Header.Set(headerAccessTS, ts)
	if len(bodyBytes) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("weex %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("weex %s %s: leyendo respuesta: %w", method, path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("weex %s %s: http %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	var env weexEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("weex %s %s: decodificando respuesta: %w", method, path, err)
	}
	payload := env.Data
	if len(payload) == 0 {
		payload = env.Result
	}
	if len(payload) == 0 {
		payload = data
	}
	if !env.Success && env.Code != "" && env.Code != weexSuccessCode && env.Code != "200" {
		return fmt.Errorf("weex %s: error %s: %s", path, env.Code, firstNonEmpty(env.Message, env.Msg))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(payload, out)
}

type weexEnvelope struct {
	Code    string          `json:"code"`
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Msg     string          `json:"msg"`
	Data    json.RawMessage `json:"data"`
	Result  json.RawMessage `json:"result"`
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}