package phase1_signal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Series represents a single Prometheus time-series result with metric labels and values.
type Series struct {
	Metric map[string]string `json:"metric"`
	Values [][]interface{}   `json:"values"`
}

/*
prometheus response struct
*/
type RangeResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []Series `json:"result"`
	} `json:"data"`
}

// Poller fetches Prometheus range queries on a fixed step and routes each
// data point through the phase1 Manager for signal processing.
//
// OnIngest (optional): called after each successful ingestion with the
// group ID, timestamp, and value. Used by MetricsStore to track live groups.
type Poller struct {
	baseURL string
	query   string

	step time.Duration

	client  *http.Client
	manager *Manager

	lastEnd time.Time

	// OnIngest is an optional callback invoked after every data point is
	// ingested. The MetricsStore registers itself here so it knows which
	// service groups are active and can export datasets to the pipeline.
	OnIngest func(groupID string, ts float64, value float64)
}

/*
init
*/
func NewPoller(urlStr, query string, step time.Duration, m *Manager) *Poller {
	return &Poller{
		baseURL: urlStr,
		query:   query,
		step:    step,
		manager: m,
		client: &http.Client{
			Timeout: 2 * time.Second,
		},
		lastEnd: time.Now(),
	}
}

/*
stable signal id (sorted labels)
*/
func buildSignalID(metric map[string]string) string {

	keys := make([]string, 0, len(metric))
	for k := range metric {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	id := ""
	for _, k := range keys {
		id += k + "=" + metric[k] + "|"
	}

	return id
}

/*
better group id (service level grouping)
*/
func buildGroupID(metric map[string]string) string {

	if svc, ok := metric["service"]; ok {
		return svc
	}
	if job, ok := metric["job"]; ok {
		return job
	}
	return "default"
}

/*
range fetch
*/
func (p *Poller) fetch(ctx context.Context, start, end time.Time) (*RangeResponse, error) {

	escaped := url.QueryEscape(p.query)

	fullURL := fmt.Sprintf(
		"%s/api/v1/query_range?query=%s&start=%d&end=%d&step=%d",
		p.baseURL,
		escaped,
		start.Unix(),
		end.Unix(),
		int(p.step.Seconds()),
	)

	req, _ := http.NewRequestWithContext(ctx, "GET", fullURL, nil)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// http status check
	if resp.StatusCode != 200 {
		return nil, errors.New("bad status code")
	}

	var pr RangeResponse
	err = json.NewDecoder(resp.Body).Decode(&pr)
	if err != nil {
		return nil, err
	}

	if pr.Status != "success" {
		return nil, errors.New("prometheus error")
	}

	return &pr, nil
}

/*
worker pool processing -- routes each Prometheus series to:
 1. A fine-grained signal-level processor (keyed by full label set).
 2. A group-level processor (keyed by service/job label) for MetricsStore aggregation.
 3. The OnIngest callback so MetricsStore tracks active groups.
*/
func (p *Poller) processSeries(series []Series) {

	wg := sync.WaitGroup{}
	sem := make(chan struct{}, 10) // max 10 parallel

	for _, s := range series {

		sem <- struct{}{}
		wg.Add(1)

		go func(s Series) {
			defer wg.Done()
			defer func() { <-sem }()

			signalID := buildSignalID(s.Metric)
			groupID := buildGroupID(s.Metric)

			// Fine-grained signal processor (existing behaviour -- unchanged).
			proc := p.manager.GetProcessor(signalID)

			for _, pt := range s.Values {

				ts, ok := pt[0].(float64)
				if !ok {
					continue
				}

				valStr, ok := pt[1].(string)
				if !ok {
					continue
				}

				v, err := strconv.ParseFloat(valStr, 64)
				if err != nil {
					continue
				}

				// fixed feature space
				values := map[string]float64{
					"value": v,
				}

				_ = proc.Ingest(SignalInput{
					SignalID: signalID,
					GroupID:  groupID,
					Time:     ts,
					Values:   values,
				})

				// Group-level processor: allows MetricsStore to retrieve an
				// aggregated time-series per service group via GetProcessor(groupID).
				groupProc := p.manager.GetProcessor(groupID)
				_ = groupProc.Ingest(SignalInput{
					SignalID: groupID,
					GroupID:  groupID,
					Time:     ts,
					Values:   values,
				})

				// Notify MetricsStore (or any registered observer) that new
				// data has arrived for this group.
				if p.OnIngest != nil {
					p.OnIngest(groupID, ts, v)
				}
			}

		}(s)
	}

	wg.Wait()
}

/*
main loop
*/
func (p *Poller) Start(ctx context.Context) {

	backoff := time.Second

	ticker := time.NewTicker(p.step)

	for {
		select {

		case <-ctx.Done():
			fmt.Println("poller stopped")
			return

		case <-ticker.C:

			now := time.Now()

			start := p.lastEnd
			end := now

			res, err := p.fetch(ctx, start, end)
			if err != nil {

				fmt.Println("fetch error:", err)

				// exponential backoff + jitter
				jitter := time.Duration(rand.Intn(500)) * time.Millisecond
				time.Sleep(backoff + jitter)

				if backoff < 10*time.Second {
					backoff *= 2
				}
				continue
			}

			// reset backoff
			backoff = time.Second

			// process data
			p.processSeries(res.Data.Result)

			// update cursor
			p.lastEnd = end
		}
	}
}
