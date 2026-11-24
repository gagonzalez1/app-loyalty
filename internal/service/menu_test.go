package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

func TestMenuPDFValidation(t *testing.T) {
	for _, tt := range []struct {
		name  string
		pages int
		err   error
	}{{"menu.pdf", 1, nil}, {"menu-30.pdf", 30, nil}, {"menu-31.pdf", 31, nil}, {"menu-scanned.pdf", 1, nil}, {"menu-protected-pypdf.pdf", 0, ErrMenuPDFPassword}} {
		t.Run(tt.name, func(t *testing.T) {
			body, err := os.ReadFile("testdata/" + tt.name)
			if err != nil {
				t.Fatal(err)
			}
			pages, sum, err := validateMenuPDF(t.Context(), body)
			if !errors.Is(err, tt.err) || pages != tt.pages {
				t.Fatalf("pages=%d err=%v", pages, err)
			}
			if err == nil && len(sum) != 32 {
				t.Fatal("missing SHA256")
			}
		})
	}
	for _, tt := range []struct {
		body []byte
		err  error
	}{{[]byte("not a PDF"), ErrMenuPDFType}, {[]byte("%PDF-1.4\nfake"), ErrMenuPDFInvalid}, {bytes.Repeat([]byte{'x'}, int(MaxMenuPDFBytes)+1), ErrMenuPDFTooLarge}} {
		if _, _, err := validateMenuPDF(t.Context(), tt.body); !errors.Is(err, tt.err) {
			t.Fatalf("err=%v want=%v", err, tt.err)
		}
	}
	body, err := os.ReadFile("testdata/menu.pdf")
	if err != nil {
		t.Fatal(err)
	}
	// The literal is page content, not an encryption trailer entry. Preserve
	// stream length and xref offsets so this remains a valid unencrypted PDF.
	original := []byte("Puntazo QA menu PDF")
	literal := []byte("Menu /Encrypt text!")
	if len(original) != len(literal) || !bytes.Contains(body, original) {
		t.Fatal("literal-content fixture must preserve PDF byte offsets")
	}
	plainWithLiteral := bytes.Replace(body, original, literal, 1)
	if pages, _, err := validateMenuPDF(t.Context(), plainWithLiteral); err != nil || pages != 1 {
		t.Fatalf("literal /Encrypt falsely treated as encryption: pages=%d err=%v", pages, err)
	}
	for _, userPassword := range []string{"secret", ""} {
		conf, err := api.LoadConfiguration(api.ConfigurationOptions{Mode: api.ConfigurationModeStateless})
		if err != nil {
			t.Fatal(err)
		}
		conf.UserPW = userPassword
		conf.OwnerPW = "owner-secret"
		var encrypted bytes.Buffer
		if err = api.Encrypt(t.Context(), bytes.NewReader(body), &encrypted, conf); err != nil {
			t.Fatal(err)
		}
		if _, _, err = validateMenuPDF(t.Context(), encrypted.Bytes()); !errors.Is(err, ErrMenuPDFPassword) {
			t.Fatalf("encrypted with userpw=%t err=%v", userPassword != "", err)
		}
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err = validateMenuPDF(cancelled, body); err == nil {
		t.Fatal("cancelled parser succeeded")
	}
}
func TestMenuPDFAcceptsUnwrappedRDFMetadata(t *testing.T) {
	// Some PDF exporters emit RDF directly instead of wrapping it in x:xmpmeta.
	metadata := `<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description rdf:about=""/></rdf:RDF>`
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R /Metadata 4 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Resources << >> >>",
		fmt.Sprintf("<< /Type /Metadata /Subtype /XML /Length %d >>\nstream\n%s\nendstream", len(metadata), metadata),
	}
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	offsets := make([]int, 0, len(objects))
	for i, object := range objects {
		offsets = append(offsets, pdf.Len())
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)

	pages, sum, err := validateMenuPDF(t.Context(), pdf.Bytes())
	if err != nil || pages != 1 || len(sum) != 32 {
		t.Fatalf("RDF metadata PDF: pages=%d hash size=%d err=%v", pages, len(sum), err)
	}
}

func TestMenuFileNameSanitization(t *testing.T) {
	for _, tt := range []struct{ in, want string }{{"../../menu.pdf", "menu.pdf"}, {`C:\fakepath\carta.pdf`, "carta.pdf"}, {"\x00\n.pdf", "menu.pdf"}, {"carta", "carta.pdf"}, {"Café.pdf", "Café.pdf"}} {
		if got := menuFileName(tt.in); got != tt.want {
			t.Fatalf("got=%q want=%q", got, tt.want)
		}
	}
	if got := menuFileName(strings.Repeat("á", 200) + ".pdf"); len(got) > 160 || !strings.HasSuffix(got, ".pdf") {
		t.Fatalf("name=%q", got)
	}
}

// Build a real single-page RGB scan near the upload boundary, rather than a
// small PDF followed by padding. The whitespace before objects fills the exact
// file size while xref offsets continue to describe the actual image stream.
func scannedMenuPDFAtSize(t *testing.T, target int) []byte {
	t.Helper()
	const width, height = 3200, 3200
	const imageBytes = width * height * 3
	render := func(padding int) []byte {
		var out bytes.Buffer
		out.Grow(target)
		out.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
		out.Write(bytes.Repeat([]byte{' '}, padding))
		out.WriteByte('\n')
		offsets := []int{0}
		object := func(number int, content string) {
			offsets = append(offsets, out.Len())
			fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", number, content)
		}
		object(1, "<< /Type /Catalog /Pages 2 0 R >>")
		object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
		object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 600 600] /Resources << /XObject << /Scan 5 0 R >> >> /Contents 4 0 R >>")
		content := "q 600 0 0 600 0 0 cm /Scan Do Q\n"
		object(4, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content))
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "5 0 obj\n<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Length %d >>\nstream\n", width, height, imageBytes)
		row := make([]byte, width*3)
		for i := 0; i < height; i++ {
			out.Write(row)
		}
		out.WriteString("\nendstream\nendobj\n")
		xref := out.Len()
		out.WriteString("xref\n0 6\n0000000000 65535 f \n")
		for _, offset := range offsets[1:] {
			fmt.Fprintf(&out, "%010d 00000 n \n", offset)
		}
		fmt.Fprintf(&out, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
		return out.Bytes()
	}
	padding := 0
	for attempt := 0; attempt < 3; attempt++ {
		body := render(padding)
		if len(body) == target {
			return body
		}
		padding += target - len(body)
		if padding < 0 {
			t.Fatal("scan fixture exceeds target")
		}
	}
	t.Fatal("unable to construct exact-size scan")
	return nil
}

func TestMenuPDFThirtyMiBBoundary(t *testing.T) {
	const maximum = 30 * 1024 * 1024
	body := scannedMenuPDFAtSize(t, maximum)
	for _, size := range []int{maximum - 1, maximum} {
		started := time.Now()
		pages, sum, err := validateMenuPDF(t.Context(), body[:size])
		if err != nil || pages != 1 || len(sum) != 32 {
			t.Fatalf("valid RGB scan size=%d pages=%d checksum=%d err=%v", size, pages, len(sum), err)
		}
		t.Logf("validated %d-byte RGB scan in %s", size, time.Since(started))
	}
	if _, _, err := validateMenuPDF(t.Context(), append(body, '\n')); !errors.Is(err, ErrMenuPDFTooLarge) {
		t.Fatalf("30 MiB + 1 must be too large: %v", err)
	}
}
