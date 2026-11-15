package notes

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestWritePDFMath: формулы отрисованы mitex (шрифт New Computer Math в PDF),
// а не напечатаны исходником.
func TestWritePDFMath(t *testing.T) {
	typstForPDF(t)
	pdf := filepath.Join(t.TempDir(), "notes.pdf")
	md := "Формула: $\\frac{a}{b} + \\sqrt{x}$\n\n$$\n\\sum_{i=1}^{n} x_i\n$$\n"
	if err := WritePDF(context.Background(), "Тема", md, pdf); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(pdf)
	if err != nil {
		t.Fatal(err)
	}
	// Математический шрифт в PDF есть только если формулу разобрал mitex.
	if !bytes.Contains(raw, []byte("NewCMMath")) {
		t.Fatal("formulas not rendered: no math font in pdf")
	}
}

// TestWritePDFBadFormulaKeepsPDF: одна формула, которую mitex не разобрал,
// не должна ронять весь конспект: она печатается исходником, остальное рисуется.
func TestWritePDFBadFormulaKeepsPDF(t *testing.T) {
	typstForPDF(t)
	pdf := filepath.Join(t.TempDir(), "notes.pdf")
	md := "Хорошая $\\frac{a}{b}$ и плохая $\\unknowncmd{x}$.\n\n$$\n\\sum_{i=1}^{n} x_i\n$$\n"
	if err := WritePDF(context.Background(), "Тема", md, pdf); err != nil {
		t.Fatalf("конспект с одной битой формулой не напечатался: %v", err)
	}
	raw, err := os.ReadFile(pdf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("NewCMMath")) {
		t.Fatal("хорошие формулы тоже не отрисованы")
	}
	if _, err := exec.LookPath("pdftotext"); err == nil {
		out, err := exec.Command("pdftotext", "-layout", pdf, "-").Output()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), "unknowncmd") {
			t.Errorf("битая формула должна быть напечатана исходником: %q", out)
		}
	}
}

// TestBadFormulasFindsOnlyBroken: поиск находит ровно битую формулу.
func TestBadFormulasFindsOnlyBroken(t *testing.T) {
	typstForPDF(t)
	dir := t.TempDir()
	if err := writeTypstWorkspace(dir, "Тема", "Ок $x^2$, сломано $\\unknowncmd{x}$, и снова ок $\\frac{a}{b}$.\n"); err != nil {
		t.Fatal(err)
	}
	got := badFormulas(context.Background(), dir)
	want := []string{`\unknowncmd{x}`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("badFormulas = %q, want %q", got, want)
	}
}
