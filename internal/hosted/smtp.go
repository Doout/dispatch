package hosted

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

type SMTPConfig struct {
	Address  string
	Username string
	Password string
	From     string
}

func (c SMTPConfig) Validate() error {
	if c.Address == "" {
		return nil
	}
	if _, _, err := net.SplitHostPort(c.Address); err != nil {
		return errors.New("SMTP address must include a host and port")
	}
	if _, err := mail.ParseAddress(c.From); err != nil || strings.ContainsAny(c.From, "\r\n") {
		return errors.New("SMTP sender must be an email address")
	}
	return nil
}

func (c SMTPConfig) SendVerification(ctx context.Context, email, link string) error {
	if c.Address == "" {
		return errors.New("email delivery is not configured")
	}
	recipient, err := mail.ParseAddress(email)
	if err != nil || strings.ContainsAny(email, "\r\n") {
		return errors.New("invalid email recipient")
	}
	sender, _ := mail.ParseAddress(c.From)
	host, _, _ := net.SplitHostPort(c.Address)
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", c.Address)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline := time.Now().Add(20 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); !ok {
		return errors.New("SMTP server must support STARTTLS")
	}
	if err = client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
		return err
	}
	if c.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", c.Username, c.Password, host)); err != nil {
			return err
		}
	}
	if err = client.Mail(sender.Address); err != nil {
		return err
	}
	if err = client.Rcpt(recipient.Address); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "From: %s\r\nTo: %s\r\nSubject: Verify your Dispatch email\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nVerify your email to use Dispatch:\r\n%s\r\n\r\nThis link expires in 30 minutes.\r\n", sender.String(), recipient.String(), link)
	closeErr := writer.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return client.Quit()
}
