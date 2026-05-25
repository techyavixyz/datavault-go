// Notification dispatcher: SMTP, Slack, Google Chat, and generic webhooks.
package services

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/smtp"
	"strings"
	"time"

	"datavault/models"
	"datavault/store"
)

// TestNotification sends a sample success message through the given channel config.
func TestNotification(ch models.NotificationChannel) error {
	now := time.Now().UTC()
	rec := &models.BackupRecord{
		SourceName:      "sample-source",
		DBType:          "postgresql",
		DestinationName: "sample-destination",
		StorageType:     "s3",
		Status:          "success",
		FileName:        "sample-backup-2026-01-01.tar.gz",
		StartedAt:       now.Add(-90 * time.Second),
		FinishedAt:      &now,
	}
	return dispatchNotification(ch, rec, "[DataVault Test]")
}

// SendNotifications dispatches alerts for a finished backup to all configured channels.
func SendNotifications(st *store.Store, channelIDs []string, rec *models.BackupRecord, jobName string) {
	for _, id := range channelIDs {
		var ch models.NotificationChannel
		if ok, _ := st.GetByID(store.TableNotifications, id, &ch); !ok {
			continue
		}
		if rec.Status == "success" && !ch.OnSuccess {
			continue
		}
		if rec.Status == "failed" && !ch.OnFailure {
			continue
		}
		if err := dispatchNotification(ch, rec, jobName); err != nil {
			log.Printf("notification %q (%s): %v", ch.Name, ch.Type, err)
		}
	}
}

func dispatchNotification(ch models.NotificationChannel, rec *models.BackupRecord, jobName string) error {
	switch ch.Type {
	case "smtp":
		return sendSMTP(ch, rec, jobName)
	case "slack":
		return postJSON(ch.Config["webhook_url"], buildSlackPayload(rec, jobName))
	case "googlechat":
		return postJSON(ch.Config["webhook_url"], buildGoogleChatPayload(rec, jobName))
	case "webhook":
		return sendWebhook(ch, rec, jobName)
	}
	return fmt.Errorf("unknown channel type: %s", ch.Type)
}

// ── Shared helpers ────────────────────────────────────────────────────────────

func isSuccess(rec *models.BackupRecord) bool { return rec.Status == "success" }

func statusLabel(rec *models.BackupRecord) string {
	if isSuccess(rec) {
		return "Success"
	}
	return "Failed"
}

func duration(rec *models.BackupRecord) string {
	if rec.FinishedAt == nil {
		return "—"
	}
	return rec.FinishedAt.Sub(rec.StartedAt).Truncate(time.Second).String()
}

func size(rec *models.BackupRecord) string {
	if rec.SizeBytes == nil || *rec.SizeBytes == 0 {
		return "—"
	}
	return fmtBytes(*rec.SizeBytes)
}

func buildSubject(rec *models.BackupRecord, jobName string) string {
	ts := FormatTime(rec.StartedAt)
	tz := ServerLocation().String()
	if isSuccess(rec) {
		return fmt.Sprintf("[DataVault] Backup Successful: %s (%s %s)", jobName, ts, tz)
	}
	return fmt.Sprintf("[DataVault] Backup Failed: %s (%s %s)", jobName, ts, tz)
}

// ── Slack Block Kit ───────────────────────────────────────────────────────────

func buildSlackPayload(rec *models.BackupRecord, jobName string) map[string]any {
	color := "#dc2626"
	icon := "❌"
	statusBadge := "✗ Failed"
	if isSuccess(rec) {
		color = "#22c55e"
		icon = "✅"
		statusBadge = "✓ Success"
	}
	title := fmt.Sprintf("%s Backup %s: %s", icon, statusLabel(rec), jobName)
	desc := fmt.Sprintf("Backup job '%s' completed successfully.", jobName)
	if !isSuccess(rec) {
		desc = fmt.Sprintf("Backup job '%s' failed. Please check the logs.", jobName)
	}

	// Build table rows as a single text block (mimics the email table layout)
	type row struct{ label, value string }
	rows := []row{
		{"JOB", jobName},
		{"SOURCE", fmt.Sprintf("%s (%s)", rec.SourceName, rec.DBType)},
		{"DESTINATION", fmt.Sprintf("%s (%s)", rec.DestinationName, rec.StorageType)},
		{"FILE", rec.FileName},
		{"DURATION", duration(rec)},
		{"SIZE", size(rec)},
		{"TIME", FormatTime(rec.StartedAt) + " " + ServerLocation().String()},
	}
	if !isSuccess(rec) && rec.Error != "" {
		rows = append(rows, row{"ERROR", rec.Error})
	}

	var tableText strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&tableText, "*%-14s* %s\n", r.label, r.value)
	}

	blocks := []map[string]any{
		{
			"type": "section",
			"text": map[string]any{
				"type": "mrkdwn",
				"text": fmt.Sprintf("*%s*\n%s   `%s`", title, desc, statusBadge),
			},
		},
		{"type": "divider"},
		{
			"type": "section",
			"text": map[string]any{"type": "mrkdwn", "text": tableText.String()},
		},
		{"type": "divider"},
		{
			"type":     "context",
			"elements": []map[string]any{{"type": "mrkdwn", "text": "Sent by *DataVault*"}},
		},
	}

	return map[string]any{
		"attachments": []map[string]any{
			{"color": color, "fallback": title, "blocks": blocks},
		},
	}
}

// ── Google Chat Cards v2 ──────────────────────────────────────────────────────

func buildGoogleChatPayload(rec *models.BackupRecord, jobName string) map[string]any {
	headerIcon := "❌"
	subtitle := fmt.Sprintf("Backup job '%s' failed. Please check the logs.", jobName)
	if isSuccess(rec) {
		headerIcon = "✅"
		subtitle = fmt.Sprintf("Backup job '%s' completed successfully.", jobName)
	}

	type row struct{ label, value string }
	rows := []row{
		{"JOB", jobName},
		{"SOURCE", fmt.Sprintf("%s (%s)", rec.SourceName, rec.DBType)},
		{"DESTINATION", fmt.Sprintf("%s (%s)", rec.DestinationName, rec.StorageType)},
		{"FILE", rec.FileName},
		{"DURATION", duration(rec)},
		{"SIZE", size(rec)},
		{"TIME", FormatTime(rec.StartedAt) + " " + ServerLocation().String()},
	}
	if !isSuccess(rec) && rec.Error != "" {
		rows = append(rows, row{"ERROR", rec.Error})
	}

	widgets := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		widgets = append(widgets, map[string]any{
			"decoratedText": map[string]any{"topLabel": r.label, "text": r.value},
		})
	}

	return map[string]any{
		"cardsV2": []map[string]any{
			{
				"cardId": "datavault-notification",
				"card": map[string]any{
					"header": map[string]any{
						"title":    fmt.Sprintf("%s Backup %s: %s", headerIcon, statusLabel(rec), jobName),
						"subtitle": subtitle,
					},
					"sections": []map[string]any{
						{"widgets": widgets},
						{
							"widgets": []map[string]any{
								{"textParagraph": map[string]any{"text": "<font color=\"#888888\">Sent by <b>DataVault</b></font>"}},
							},
						},
					},
				},
			},
		},
	}
}

// SendPlainEmail sends a plain-text email using the first SMTP notification channel found in the store.
// Used for system emails (e.g. forgot-password) that are not tied to a backup record.
func SendPlainEmail(st *store.Store, to, subject, body string) error {
	var channels []models.NotificationChannel
	st.GetAll(store.TableNotifications, &channels)
	for _, ch := range channels {
		if ch.Type == "smtp" {
			return sendSMTPRaw(ch, []string{to}, subject, body)
		}
	}
	return fmt.Errorf("no SMTP notification channel configured — add one in Notifications settings")
}

// ── SMTP ──────────────────────────────────────────────────────────────────────

func sendSMTP(ch models.NotificationChannel, rec *models.BackupRecord, jobName string) error {
	toRaw := ch.Config["to"]
	to := splitTrim(toRaw)
	if len(to) == 0 {
		return fmt.Errorf("smtp: no recipients configured")
	}
	senderName := ch.Config["sender_name"]
	if senderName == "" {
		senderName = "DataVault"
	}
	return sendSMTPRaw(ch, to, buildSubject(rec, jobName), buildHTMLEmail(rec, jobName), senderName)
}

func sendSMTPRaw(ch models.NotificationChannel, to []string, subject, htmlBody string, senderName ...string) error {
	host := ch.Config["host"]
	port := ch.Config["port"]
	if port == "" {
		port = "587"
	}
	username := ch.Config["username"]
	password := ch.Config["password"]
	from := ch.Config["from"]
	tlsMode := ch.Config["tls"]
	if tlsMode == "" {
		tlsMode = "starttls"
	}
	if host == "" {
		return fmt.Errorf("smtp: host not configured")
	}

	name := "DataVault"
	if len(senderName) > 0 && senderName[0] != "" {
		name = senderName[0]
	}
	msg := buildMIME(from, to, subject, htmlBody, name)
	addr := host + ":" + port

	var auth smtp.Auth
	if username != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}

	if tlsMode == "tls" {
		tlsCfg := &tls.Config{ServerName: host}
		conn, err := tls.Dial("tcp", addr, tlsCfg)
		if err != nil {
			return fmt.Errorf("smtp tls dial: %w", err)
		}
		client, err := smtp.NewClient(conn, host)
		if err != nil {
			return fmt.Errorf("smtp client: %w", err)
		}
		defer client.Close()
		if auth != nil {
			if err := client.Auth(auth); err != nil {
				return fmt.Errorf("smtp auth: %w", err)
			}
		}
		if err := client.Mail(from); err != nil {
			return err
		}
		for _, r := range to {
			if err := client.Rcpt(r); err != nil {
				return err
			}
		}
		w, err := client.Data()
		if err != nil {
			return err
		}
		_, err = w.Write(msg)
		w.Close()
		return err
	}

	return smtp.SendMail(addr, auth, from, to, msg)
}

func buildMIME(from string, to []string, subject, htmlBody, senderName string) []byte {
	fromHeader := from
	if !strings.Contains(from, "<") {
		fromHeader = fmt.Sprintf("%s <%s>", senderName, from)
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: %s\r\n", fromHeader)
	fmt.Fprintf(&buf, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&buf, "Subject: %s\r\n", subject)
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: text/html; charset=utf-8\r\n")
	fmt.Fprintf(&buf, "\r\n")
	buf.WriteString(htmlBody)
	return buf.Bytes()
}

func buildHTMLEmail(rec *models.BackupRecord, jobName string) string {
	success := isSuccess(rec)
	statusText := statusLabel(rec)
	statusColor := "#dc2626"
	statusBg := "#fef2f2"
	statusDot := "&#10060;"
	titlePrefix := "Backup Failed"
	descText := fmt.Sprintf("Backup job '%s' failed. Please check the logs for details.", jobName)
	if success {
		statusColor = "#16a34a"
		statusBg = "#f0fdf4"
		statusDot = "&#10003;"
		titlePrefix = "Backup Successful"
		descText = fmt.Sprintf("Backup job '%s' completed successfully.", jobName)
	}

	rows := []struct{ label, value string }{
		{"JOB", jobName},
		{"SOURCE", fmt.Sprintf("%s (%s)", rec.SourceName, rec.DBType)},
		{"DESTINATION", fmt.Sprintf("%s (%s)", rec.DestinationName, rec.StorageType)},
		{"FILE", rec.FileName},
		{"DURATION", duration(rec)},
		{"SIZE", size(rec)},
		{"TIME", FormatTime(rec.StartedAt) + " " + ServerLocation().String()},
	}
	if !success && rec.Error != "" {
		rows = append(rows, struct{ label, value string }{"ERROR", rec.Error})
	}

	var tableRows strings.Builder
	for i, r := range rows {
		bg := "#ffffff"
		if i%2 == 0 {
			bg = "#f9fafb"
		}
		fmt.Fprintf(&tableRows,
			`<tr style="background:%s"><td style="padding:11px 18px;font-size:11px;font-weight:700;color:#6b7280;text-transform:uppercase;letter-spacing:.6px;white-space:nowrap;border-right:1px solid #e5e7eb;width:120px">%s</td><td style="padding:11px 18px;font-size:13px;color:#111827">%s</td></tr>`,
			bg, r.label, r.value)
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"/><meta name="viewport" content="width=device-width,initial-scale=1.0"/><title>%s: %s</title></head>
<body style="margin:0;padding:0;background:#f3f4f6;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif">
<table width="100%%" cellpadding="0" cellspacing="0" style="background:#f3f4f6;padding:32px 0">
  <tr><td align="center">
    <table width="560" cellpadding="0" cellspacing="0" style="background:#ffffff;border-radius:12px;overflow:hidden;box-shadow:0 2px 12px rgba(0,0,0,.08);max-width:560px;width:100%%">

      <!-- Red header -->
      <tr>
        <td style="background:#dc2626;padding:20px 28px">
          <table width="100%%" cellpadding="0" cellspacing="0">
            <tr>
              <td>
                <table cellpadding="0" cellspacing="0">
                  <tr>
                    <td style="background:rgba(255,255,255,.18);border-radius:8px;width:32px;height:32px;text-align:center;vertical-align:middle;padding:0 8px">
                      <span style="color:#fff;font-size:16px;line-height:32px">&#9670;</span>
                    </td>
                    <td style="padding-left:10px;vertical-align:middle">
                      <div style="color:#fff;font-size:16px;font-weight:700;line-height:1.2">DataVault</div>
                      <div style="color:rgba(255,255,255,.75);font-size:10px;font-weight:500;letter-spacing:.8px;text-transform:uppercase">BACKUP MANAGER</div>
                    </td>
                  </tr>
                </table>
              </td>
              <td align="right" style="vertical-align:middle">
                <span style="background:%s;color:%s;border-radius:20px;padding:4px 12px;font-size:11px;font-weight:700;letter-spacing:.3px">%s %s</span>
              </td>
            </tr>
          </table>
        </td>
      </tr>

      <!-- Body -->
      <tr>
        <td style="padding:28px 28px 20px">
          <div style="font-size:20px;font-weight:700;color:#111827;margin-bottom:6px">%s: %s</div>
          <div style="font-size:13.5px;color:#6b7280">%s</div>
        </td>
      </tr>

      <!-- Table -->
      <tr>
        <td style="padding:0 28px 28px">
          <table width="100%%" cellpadding="0" cellspacing="0" style="border:1px solid #e5e7eb;border-radius:8px;overflow:hidden;border-collapse:collapse">
            %s
          </table>
        </td>
      </tr>

      <!-- Footer -->
      <tr>
        <td style="border-top:1px solid #f3f4f6;padding:16px 28px;text-align:center">
          <span style="font-size:12px;color:#9ca3af">Sent by <span style="color:#dc2626;font-weight:600">DataVault</span></span>
        </td>
      </tr>

    </table>
  </td></tr>
</table>
</body>
</html>`,
		titlePrefix, jobName,
		statusBg, statusColor, statusDot, statusText,
		titlePrefix, jobName,
		descText,
		tableRows.String(),
	)
}

// ── Slack / Google Chat ───────────────────────────────────────────────────────

func postJSON(url string, payload any) error {
	if url == "" {
		return fmt.Errorf("webhook_url not configured")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	return nil
}

// ── Generic webhook ───────────────────────────────────────────────────────────

func sendWebhook(ch models.NotificationChannel, rec *models.BackupRecord, jobName string) error {
	url := ch.Config["url"]
	method := ch.Config["method"]
	if url == "" {
		return fmt.Errorf("webhook url not configured")
	}
	if method == "" {
		method = "POST"
	}

	payload := map[string]any{
		"event":       "backup." + rec.Status,
		"status":      rec.Status,
		"job":         jobName,
		"source":      rec.SourceName,
		"db_type":     string(rec.DBType),
		"destination": rec.DestinationName,
		"storage":     string(rec.StorageType),
		"file":        rec.FileName,
		"size":        size(rec),
		"duration":    duration(rec),
		"time":        rec.StartedAt.UTC().Format(time.RFC3339),
		"error":       rec.Error,
	}

	var body io.Reader
	if method == "POST" {
		data, _ := json.Marshal(payload)
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return err
	}
	if method == "POST" {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func fmtBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func splitTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
