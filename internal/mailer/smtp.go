package mailer

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"

	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/model"
)

type SMTPSender struct{ cfg config.Config }

func NewSMTP(cfg config.Config) *SMTPSender { return &SMTPSender{cfg: cfg} }
func (s *SMTPSender) Send(ctx context.Context, message model.EmailMessage) error {
	if strings.ContainsAny(message.To+message.Subject, "\r\n") {
		return errors.New("unsafe mail header")
	}
	address := net.JoinHostPort(s.cfg.SMTPHost, fmt.Sprint(s.cfg.SMTPPort))
	dialer := net.Dialer{Timeout: 10 * time.Second}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: s.cfg.SMTPHost}
	var conn net.Conn
	var err error
	if s.cfg.SMTPTLSMode == "tls" {
		conn, err = tls.DialWithDialer(&dialer, "tcp", address, tlsConfig)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return fmt.Errorf("dial smtp: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	c, err := smtp.NewClient(conn, s.cfg.SMTPHost)
	if err != nil {
		return err
	}
	defer c.Close()
	if s.cfg.SMTPTLSMode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("smtp server lacks STARTTLS")
		}
		if err = c.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if s.cfg.SMTPUsername != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("smtp server lacks AUTH")
		}
		if err = c.Auth(smtp.PlainAuth("", s.cfg.SMTPUsername, s.cfg.SMTPPassword, s.cfg.SMTPHost)); err != nil {
			return err
		}
	}
	if err = c.Mail(s.cfg.MailFromAddress); err != nil {
		return err
	}
	if err = c.Rcpt(message.To); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err = w.Write(encodeMessage(s.cfg.MailFromName, s.cfg.MailFromAddress, message)); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
func encodeMessage(fromName, fromAddress string, m model.EmailMessage) []byte {
	alternativeBoundary := "puntazo-alternative"
	relatedBoundary := "puntazo-related"
	var b bytes.Buffer
	contentType := fmt.Sprintf("multipart/alternative; boundary=%q", alternativeBoundary)
	if len(m.InlineImages) > 0 {
		contentType = fmt.Sprintf("multipart/related; boundary=%q", relatedBoundary)
	}
	fmt.Fprintf(&b, "From: %s <%s>\r\nTo: <%s>\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: %s\r\n\r\n", mime.QEncoding.Encode("UTF-8", fromName), fromAddress, m.To, mime.QEncoding.Encode("UTF-8", m.Subject), contentType)
	if len(m.InlineImages) > 0 {
		fmt.Fprintf(&b, "--%s\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n", relatedBoundary, alternativeBoundary)
	}
	write := func(kind, body string) {
		fmt.Fprintf(&b, "--%s\r\nContent-Type: %s; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n", alternativeBoundary, kind)
		writeBase64(&b, []byte(body))
	}
	write("text/plain", m.Text)
	write("text/html", m.HTML)
	fmt.Fprintf(&b, "--%s--\r\n", alternativeBoundary)
	for _, image := range m.InlineImages {
		if len(image.Data) == 0 || strings.ContainsAny(image.ContentID+image.Filename+image.ContentType, "\r\n") {
			continue
		}
		fmt.Fprintf(&b, "--%s\r\nContent-Type: %s; name=%q\r\nContent-Transfer-Encoding: base64\r\nContent-ID: <%s>\r\nContent-Disposition: inline; filename=%q\r\n\r\n", relatedBoundary, image.ContentType, image.Filename, image.ContentID, image.Filename)
		writeBase64(&b, image.Data)
	}
	if len(m.InlineImages) > 0 {
		fmt.Fprintf(&b, "--%s--\r\n", relatedBoundary)
	}
	return b.Bytes()
}

func writeBase64(b *bytes.Buffer, data []byte) {
	raw := base64.StdEncoding.EncodeToString(data)
	for len(raw) > 76 {
		b.WriteString(raw[:76] + "\r\n")
		raw = raw[76:]
	}
	b.WriteString(raw + "\r\n")
}
