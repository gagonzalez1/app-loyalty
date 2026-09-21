package mailer

import (
	"context"
	"strings"
	"testing"

	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/model"
)

func TestMessagesContainBoundedActionLinks(t *testing.T) {
	verification := VerificationMessage("https://app.puntazo.test", "user@example.com", "secret-token")
	if !strings.Contains(verification.Text, "https://app.puntazo.test/verify-email?token=secret-token") || !strings.Contains(verification.Text, "24 horas") {
		t.Fatalf("verification=%+v", verification)
	}
	reset := PasswordResetMessage("https://app.puntazo.test", "user@example.com", "secret-token")
	if !strings.Contains(reset.Text, "/reset-password?token=secret-token") || !strings.Contains(reset.Text, "1 hora") || !strings.Contains(reset.HTML, "Creá una contraseña nueva") || !strings.Contains(reset.HTML, "cid:"+mascotContentID) || strings.Contains(reset.HTML, "%!") {
		t.Fatalf("reset=%+v", reset)
	}
	invitation := BrandInvitationMessage("https://app.puntazo.test", "operator@example.com", "invite-token", BrandInvitationDetails{BrandName: "Café Aurora", Role: "OPERADOR", Branches: []string{"Belgrano", "Palermo"}, BrandLogoURL: "https://media.puntazo.test/logo.png"})
	for _, expected := range []string{"Café Aurora quiere sumarte", "Operador", "Belgrano, Palermo", "https://media.puntazo.test/logo.png", "/invitaciones/aceptar?token=invite-token", "todavía no se haya registrado"} {
		if !strings.Contains(invitation.HTML, expected) {
			t.Fatalf("invitation HTML missing %q", expected)
		}
	}
	if strings.Contains(invitation.HTML, "%!") {
		t.Fatalf("invitation HTML contains formatting error")
	}
}

func TestEncodeMessageEmbedsInlineImagesAsRelatedParts(t *testing.T) {
	encoded := string(encodeMessage("Puntazo", "hola@puntazo.pro", model.EmailMessage{
		To:      "user@example.com",
		Subject: "Prueba",
		Text:    "texto",
		HTML:    `<img src="cid:mr-puntazo@puntazo">`,
		InlineImages: []model.EmailInlineImage{{
			ContentID: "mr-puntazo@puntazo", ContentType: "image/png", Filename: "mr-puntazo.png", Data: []byte("png"),
		}},
	}))
	for _, expected := range []string{"multipart/related", "multipart/alternative", "Content-ID: <mr-puntazo@puntazo>", "Content-Disposition: inline", "cG5n"} {
		if !strings.Contains(encoded, expected) {
			t.Fatalf("encoded message missing %q", expected)
		}
	}
}

func TestApprovedTemplatesEscapeDynamicContent(t *testing.T) {
	reset := PasswordResetMessage("https://app.puntazo.test", `<script>@example.com`, `token&unsafe`)
	if strings.Contains(reset.HTML, `<script>`) || !strings.Contains(reset.HTML, `token%26unsafe`) {
		t.Fatalf("unsafe reset HTML=%s", reset.HTML)
	}
	invitation := BrandInvitationMessage("https://app.puntazo.test", "operator@example.com", "token", BrandInvitationDetails{BrandName: `<img src=x onerror=alert(1)>`, Role: "OPERADOR"})
	if strings.Contains(invitation.HTML, `<img src=x`) || !strings.Contains(invitation.HTML, `&lt;img`) {
		t.Fatalf("unsafe invitation HTML=%s", invitation.HTML)
	}
}
func TestSMTPRejectsHeaderInjectionBeforeDial(t *testing.T) {
	sender := NewSMTP(config.Config{})
	err := sender.Send(context.Background(), model.EmailMessage{To: "safe@example.com\r\nBcc: bad@example.com", Subject: "test"})
	if err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("error=%v", err)
	}
}
