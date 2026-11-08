package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/atlas/platform/pkg/auth"
	"github.com/atlas/platform/pkg/config"
	"github.com/atlas/platform/pkg/db"
	"github.com/atlas/platform/pkg/events"
	"github.com/atlas/platform/pkg/httpx"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sestypes "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx := context.Background()

	pool, err := db.Connect(ctx, config.Must("DATABASE_URL"), false)
	if err != nil {
		slog.Error("db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := migrate(ctx, pool); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}

	bus, err := events.Connect(ctx, config.Get("NATS_URL", "nats://nats:4222"), "notification")
	if err != nil {
		slog.Error("nats", "err", err)
		os.Exit(1)
	}
	defer bus.Close()

	h := &handler{
		pool:     pool,
		bus:      bus,
		mode:     config.Get("NOTIFY_MODE", "log"),
		sesFrom:  config.Get("SES_FROM", ""),
		region:   config.Get("AWS_REGION", "us-west-2"),
		twSID:    config.Get("TWILIO_SID", ""),
		twToken:  config.Get("TWILIO_TOKEN", ""),
		twFrom:   config.Get("TWILIO_FROM", ""),
		http:     &http.Client{Timeout: 20 * time.Second},
	}

	if _, err := bus.Subscribe("notify-requested", events.NotificationRequested, h.onRequested); err != nil {
		slog.Error("subscribe", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", httpx.Healthz)
	mux.HandleFunc("GET /v1/deliveries", httpx.RequirePerm(auth.PermNotifyRead, h.list))

	if err := httpx.Serve(httpx.Middleware(config.Must("JWT_SECRET"))(mux)); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return db.ExecAll(ctx, pool,
		`CREATE TABLE IF NOT EXISTS deliveries (
			id UUID PRIMARY KEY,
			user_id UUID,
			channel TEXT NOT NULL,
			destination TEXT NOT NULL,
			subject TEXT,
			body TEXT NOT NULL,
			status TEXT NOT NULL,
			error TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
	)
}

type handler struct {
	pool     *pgxpool.Pool
	bus      *events.Bus
	mode     string
	sesFrom  string
	region   string
	twSID    string
	twToken  string
	twFrom   string
	http     *http.Client
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(),
		`SELECT id::text, channel, destination, subject, body, status, error, created_at
		 FROM deliveries WHERE user_id=$1 ORDER BY created_at DESC LIMIT 100`,
		httpx.UserID(r.Context()))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	type row struct {
		ID          string    `json:"id"`
		Channel     string    `json:"channel"`
		Destination string    `json:"destination"`
		Subject     *string   `json:"subject"`
		Body        string    `json:"body"`
		Status      string    `json:"status"`
		Error       *string   `json:"error"`
		CreatedAt   time.Time `json:"created_at"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.Channel, &x.Destination, &x.Subject, &x.Body, &x.Status, &x.Error, &x.CreatedAt); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "db error")
			return
		}
		out = append(out, x)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"deliveries": out, "mode": h.mode})
}

func (h *handler) onRequested(env events.Envelope) error {
	p, err := events.Decode[events.NotificationRequestedPayload](env)
	if err != nil {
		return err
	}
	status := "sent"
	var sendErr error
	switch strings.ToLower(p.Channel) {
	case "sms":
		sendErr = h.sendSMS(p)
	default:
		sendErr = h.sendEmail(p)
	}
	errText := ""
	if sendErr != nil {
		status = "failed"
		errText = sendErr.Error()
		slog.Error("delivery failed", "err", sendErr, "channel", p.Channel)
	}
	id := uuid.New()
	var errPtr *string
	if errText != "" {
		errPtr = &errText
	}
	_, dbErr := h.pool.Exec(context.Background(),
		`INSERT INTO deliveries (id, user_id, channel, destination, subject, body, status, error)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		id, nilIfEmpty(p.UserID), p.Channel, p.Destination, p.Subject, p.Body, status, errPtr)
	if dbErr != nil {
		return dbErr
	}
	if sendErr == nil {
		_ = h.bus.Publish(context.Background(), events.NotificationDelivered, map[string]string{
			"id": id.String(), "user_id": p.UserID, "channel": p.Channel,
		})
	}
	return nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (h *handler) sendEmail(p events.NotificationRequestedPayload) error {
	switch h.mode {
	case "ses":
		return h.sendSES(p)
	default:
		slog.Info("email (log mode)", "to", p.Destination, "subject", p.Subject, "body", p.Body)
		return nil
	}
}

func (h *handler) sendSMS(p events.NotificationRequestedPayload) error {
	switch h.mode {
	case "twilio":
		return h.sendTwilio(p)
	default:
		slog.Info("sms (log mode)", "to", p.Destination, "body", p.Body)
		return nil
	}
}

func (h *handler) sendSES(p events.NotificationRequestedPayload) error {
	if h.sesFrom == "" {
		return fmt.Errorf("SES_FROM not set")
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(h.region))
	if err != nil {
		return fmt.Errorf("aws config: %w", err)
	}
	client := sesv2.NewFromConfig(cfg)
	_, err = client.SendEmail(context.Background(), &sesv2.SendEmailInput{
		FromEmailAddress: &h.sesFrom,
		Destination:      &sestypes.Destination{ToAddresses: []string{p.Destination}},
		Content: &sestypes.EmailContent{
			Simple: &sestypes.Message{
				Subject: &sestypes.Content{Data: &p.Subject},
				Body:    &sestypes.Body{Text: &sestypes.Content{Data: &p.Body}},
			},
		},
	})
	return err
}

func (h *handler) sendTwilio(p events.NotificationRequestedPayload) error {
	if h.twSID == "" || h.twToken == "" || h.twFrom == "" {
		return fmt.Errorf("twilio credentials not set")
	}
	form := url.Values{}
	form.Set("To", p.Destination)
	form.Set("From", h.twFrom)
	form.Set("Body", p.Subject+"\n"+p.Body)
	endpoint := "https://api.twilio.com/2010-04-01/Accounts/" + h.twSID + "/Messages.json"
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.SetBasicAuth(h.twSID, h.twToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := h.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("twilio status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
