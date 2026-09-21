package mailer

import (
	"context"
	_ "embed"
	"fmt"
	"html"
	"net/url"
	"strings"
	"sync"
	"unicode"

	"clientesFrecuentes/internal/model"
)

const mascotContentID = "mr-puntazo@puntazo"
const brandLogoContentID = "brand-logo@puntazo"

//go:embed assets/mr-puntazo.png
var mascotPNG []byte

type Sender interface {
	Send(context.Context, model.EmailMessage) error
}

type MemorySender struct {
	mu       sync.Mutex
	messages []model.EmailMessage
	Err      error
}

func (m *MemorySender) Send(_ context.Context, message model.EmailMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	m.messages = append(m.messages, message)
	return nil
}

func (m *MemorySender) Messages() []model.EmailMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]model.EmailMessage(nil), m.messages...)
}

type BrandInvitationDetails struct {
	BrandName    string
	Role         string
	Branches     []string
	BrandLogoURL string
}

func VerificationMessage(appURL, to, token string) model.EmailMessage {
	link := actionURL(appURL, "/verify-email", token)
	message := transactional("VERIFY_EMAIL", to, "Verificá tu correo en Puntazo", "Verificá tu correo para activar tu cuenta: "+link+"\n\nEl enlace vence en 24 horas.", "Verificá tu correo", "Activar mi cuenta", link, "Este enlace vence en 24 horas.")
	message.Token = token
	return message
}

func PasswordResetMessage(appURL, to, token string) model.EmailMessage {
	link := actionURL(appURL, "/reset-password", token)
	text := "Recibimos una solicitud para restablecer la contraseña de tu cuenta de Puntazo.\n\nCrear nueva contraseña: " + link + "\n\nEl enlace vence en 1 hora y puede usarse una sola vez. Si no solicitaste este cambio, ignorá este correo: tu contraseña actual seguirá funcionando."
	content := fmt.Sprintf(`
        <div style="padding:34px 38px 8px;text-align:center;">
          <table role="presentation" cellspacing="0" cellpadding="0" border="0" align="center"><tr>
            <td width="150" align="center"><img src="%s" width="128" alt="Mr. Puntazo protege tu cuenta" style="display:block;width:128px;max-width:100%%;height:auto;border:0;"></td>
            <td width="46" align="center" style="font-size:25px;color:#1687ff;">→</td>
            <td width="104" height="104" align="center" style="width:104px;height:104px;border-radius:52px;background:#e8f3ff;color:#1687ff;font-size:42px;line-height:104px;">🔒</td>
          </tr></table>
        </div>
        <div style="padding:8px 38px 0;text-align:center;">
          <p style="margin:0 0 10px;color:#1687ff;font-size:13px;font-weight:800;text-transform:uppercase;letter-spacing:1px;">Recuperación de acceso</p>
          <h1 style="margin:0;color:#10213a;font-size:30px;line-height:1.18;letter-spacing:-.7px;">Creá una contraseña nueva</h1>
          <p style="margin:16px auto 0;max-width:500px;color:#50647e;font-size:16px;line-height:1.55;">Recibimos una solicitud para restablecer la contraseña de tu cuenta de Puntazo. Usá el siguiente botón para elegir una nueva.</p>
        </div>
        <div style="padding:26px 38px 0;">
          <table role="presentation" width="100%%" cellspacing="0" cellpadding="0" border="0" style="width:100%%;background:#f5f9fe;border:1px solid #dbe8f7;border-radius:16px;">
            %s%s%s
          </table>
        </div>
        %s
        <div style="padding:26px 38px 0;"><table role="presentation" width="100%%" cellspacing="0" cellpadding="0" border="0" style="width:100%%;background:#fff7e8;border:1px solid #f2d493;border-radius:14px;"><tr><td width="42" valign="top" style="padding:17px 0 17px 18px;font-size:22px;">🛡️</td><td style="padding:17px 18px 17px 8px;color:#64490e;font-size:14px;line-height:1.5;"><strong>Protegé tu cuenta:</strong> Puntazo nunca te pedirá tu contraseña ni el código de este enlace por correo, mensaje o llamada.</td></tr></table></div>
        %s`,
		"cid:"+mascotContentID,
		detailRow("Cuenta", to, true),
		detailRow("Solicitud", "Restablecer contraseña", true),
		detailRow("Validez del enlace", "1 hora", false),
		actionBlock("Crear nueva contraseña", link, "Por seguridad, este enlace vence en 1 hora y puede usarse una sola vez."),
		fallbackBlock(link, "¿No solicitaste este cambio?", "Podés ignorar este correo. Tu contraseña actual seguirá funcionando y no se realizará ningún cambio."),
	)
	body := emailShell("Restablecé tu contraseña de Puntazo", "Seguridad de cuenta", "Recibimos una solicitud para restablecer tu contraseña de Puntazo. El enlace vence en 1 hora.", content, "Este es un mensaje de seguridad; no es una comunicación promocional.")
	return model.EmailMessage{Kind: "RESET_PASSWORD", To: to, Subject: "Restablecé tu contraseña de Puntazo", Text: text, HTML: body, Token: token}
}

func BrandInvitationMessage(appURL, to, token string, details BrandInvitationDetails) model.EmailMessage {
	link := actionURL(appURL, "/invitaciones/aceptar", token)
	brandName := strings.TrimSpace(details.BrandName)
	if brandName == "" {
		brandName = "Un comercio"
	}
	role := displayRole(details.Role)
	branches := strings.Join(details.Branches, ", ")
	if branches == "" {
		branches = "A confirmar"
	}
	brandVisual := fmt.Sprintf(`<span style="display:inline-block;width:112px;height:112px;line-height:112px;border-radius:56px;background:#e8f3ff;color:#1687ff;font-size:42px;font-weight:800;text-align:center;">%s</span>`, html.EscapeString(initial(brandName)))
	if strings.TrimSpace(details.BrandLogoURL) != "" {
		brandVisual = fmt.Sprintf(`<img src="%s" width="112" height="112" alt="Logo de %s" style="display:block;width:112px;height:112px;object-fit:cover;border:0;border-radius:56px;box-shadow:0 5px 16px rgba(16,33,58,.18);">`, html.EscapeString(details.BrandLogoURL), html.EscapeString(brandName))
	}
	text := fmt.Sprintf("%s te invitó a trabajar como %s en Puntazo.\n\nSucursales: %s\nCorreo invitado: %s\n\nAceptar invitación: %s\n\nEl enlace vence en 72 horas. Creá la cuenta usando este mismo correo, que no debe haberse registrado antes en Puntazo.", brandName, strings.ToLower(role), branches, to, link)
	content := fmt.Sprintf(`
        <div style="padding:36px 38px 12px;text-align:center;"><table role="presentation" cellspacing="0" cellpadding="0" border="0" align="center"><tr>
          <td width="150" align="center"><img src="%s" width="128" alt="Mr. Puntazo te da la bienvenida" style="display:block;width:128px;max-width:100%%;height:auto;border:0;"></td>
          <td width="50" align="center" style="font-size:28px;color:#1687ff;">→</td><td width="150" align="center">%s</td>
        </tr></table></div>
        <div style="padding:8px 38px 0;text-align:center;">
          <p style="margin:0 0 10px;color:#1687ff;font-size:13px;font-weight:800;text-transform:uppercase;letter-spacing:1px;">Nueva invitación</p>
          <h1 style="margin:0;color:#10213a;font-size:30px;line-height:1.18;letter-spacing:-.7px;">%s quiere sumarte a su equipo</h1>
          <p style="margin:16px auto 0;max-width:500px;color:#50647e;font-size:16px;line-height:1.55;">Te invitaron a trabajar en Puntazo. Aceptá la invitación para crear tu acceso y comenzar a operar en las sucursales asignadas.</p>
        </div>
        <div style="padding:26px 38px 0;"><table role="presentation" width="100%%" cellspacing="0" cellpadding="0" border="0" style="width:100%%;background:#f5f9fe;border:1px solid #dbe8f7;border-radius:16px;">
          %s%s%s%s
        </table></div>
        %s
        <div style="padding:26px 38px 0;"><table role="presentation" width="100%%" cellspacing="0" cellpadding="0" border="0" style="width:100%%;background:#fff7e8;border:1px solid #f2d493;border-radius:14px;"><tr><td width="42" valign="top" style="padding:17px 0 17px 18px;font-size:22px;">✦</td><td style="padding:17px 18px 17px 8px;color:#64490e;font-size:14px;line-height:1.5;"><strong>Importante:</strong> creá la cuenta usando este mismo correo. La invitación está pensada para una dirección que todavía no se haya registrado en Puntazo.</td></tr></table></div>
        %s`,
		"cid:"+mascotContentID, brandVisual, html.EscapeString(brandName),
		detailRow("Marca", brandName, true), detailRow("Rol", role, true), detailRow("Sucursales", branches, true), detailRow("Correo invitado", to, false),
		actionBlock("Aceptar invitación", link, "Este enlace vence en 72 horas."),
		fallbackBlock(link, "Si no esperabas esta invitación", "Podés ignorar este correo. Nadie podrá acceder a tu cuenta sin completar el registro."),
	)
	body := emailShell("Invitación a "+brandName+" en Puntazo", "Invitación de equipo", brandName+" te invitó a trabajar como "+strings.ToLower(role)+" en Puntazo. La invitación vence en 72 horas.", content, "Este es un mensaje operativo; no es una comunicación promocional.")
	return model.EmailMessage{Kind: "BRAND_INVITATION", To: to, Subject: brandName + " te invitó a su equipo en Puntazo", Text: text, HTML: body, Token: token}
}

func emailShell(title, label, preheader, content, footer string) string {
	return fmt.Sprintf(`<!doctype html><html lang="es"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title></head><body style="margin:0;padding:0;background:#eef4fb;color:#10213a;font-family:Arial,Helvetica,sans-serif;"><div style="display:none;max-height:0;overflow:hidden;opacity:0;color:transparent;">%s</div><table role="presentation" width="100%%" cellspacing="0" cellpadding="0" border="0" style="width:100%%;background:#eef4fb;"><tr><td align="center" style="padding:32px 12px;"><table role="presentation" width="100%%" cellspacing="0" cellpadding="0" border="0" style="width:100%%;max-width:640px;background:#ffffff;border:1px solid #dce6f3;border-radius:24px;overflow:hidden;box-shadow:0 12px 38px rgba(16,33,58,.10);"><tr><td style="padding:22px 32px;background:#07172a;color:#ffffff;"><table role="presentation" width="100%%" cellspacing="0" cellpadding="0" border="0"><tr><td style="font-size:25px;font-weight:800;letter-spacing:-.5px;"><span style="display:inline-block;width:34px;height:34px;line-height:34px;margin-right:8px;border-radius:50%%;background:#1687ff;color:#ffffff;text-align:center;vertical-align:middle;">P</span> Puntazo</td><td align="right" style="font-size:12px;color:#9fcaff;text-transform:uppercase;letter-spacing:1.2px;">%s</td></tr></table></td></tr><tr><td>%s</td></tr></table><p style="margin:18px 0 0;color:#75869a;font-size:12px;line-height:1.5;text-align:center;">Puntazo · Cuidamos la fidelidad<br>%s</p></td></tr></table></body></html>`, html.EscapeString(title), html.EscapeString(preheader), html.EscapeString(label), content, html.EscapeString(footer))
}

func detailRow(label, value string, border bool) string {
	borderStyle := ""
	if border {
		borderStyle = "border-bottom:1px solid #dbe8f7;"
	}
	return fmt.Sprintf(`<tr><td style="padding:18px 20px;%scolor:#60738c;font-size:13px;">%s</td><td align="right" style="padding:18px 20px;%scolor:#10213a;font-size:14px;font-weight:700;">%s</td></tr>`, borderStyle, html.EscapeString(label), borderStyle, html.EscapeString(value))
}

func actionBlock(label, link, note string) string {
	safeLink := html.EscapeString(link)
	return fmt.Sprintf(`<div style="padding:28px 38px 0;text-align:center;"><a href="%s" style="display:inline-block;min-width:230px;padding:16px 26px;border-radius:999px;background:#1687ff;color:#ffffff;font-size:16px;font-weight:800;text-decoration:none;box-shadow:0 7px 18px rgba(22,135,255,.25);">%s</a><p style="margin:13px 0 0;color:#6c7e95;font-size:13px;">%s</p></div>`, safeLink, html.EscapeString(label), html.EscapeString(note))
}

func fallbackBlock(link, lead, text string) string {
	return fmt.Sprintf(`<div style="padding:26px 38px 34px;color:#6c7e95;font-size:12px;line-height:1.55;"><p style="margin:0 0 8px;">Si el botón no funciona, copiá y pegá este enlace en tu navegador:</p><p style="margin:0;padding:12px;background:#f3f6fa;border-radius:10px;word-break:break-all;color:#315378;">%s</p><p style="margin:18px 0 0;"><strong>%s:</strong> %s</p></div>`, html.EscapeString(link), html.EscapeString(lead), html.EscapeString(text))
}

func actionURL(base, path, token string) string {
	return absoluteURL(base, path) + "?token=" + url.QueryEscape(token)
}

func absoluteURL(base, path string) string {
	return strings.TrimRight(base, "/") + path
}

func displayRole(role string) string {
	switch strings.ToUpper(strings.TrimSpace(role)) {
	case "ADMINISTRADOR":
		return "Administrador"
	case "OPERADOR":
		return "Operador"
	default:
		return "Personal"
	}
}

func initial(value string) string {
	for _, char := range strings.TrimSpace(value) {
		return string(unicode.ToUpper(char))
	}
	return "P"
}

func transactional(kind, to, subject, text, title, action, link, note string) model.EmailMessage {
	h := fmt.Sprintf(`<!doctype html><html lang="es"><body><h1>%s</h1><p>%s</p><p><a href="%s">%s</a></p><p>%s</p></body></html>`, html.EscapeString(title), html.EscapeString(note), html.EscapeString(link), html.EscapeString(action), html.EscapeString(note))
	return model.EmailMessage{Kind: kind, To: to, Subject: subject, Text: text, HTML: h}
}
