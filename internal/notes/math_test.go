package notes

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
)

func TestProtectMath(t *testing.T) {
	cases := []struct {
		name, md string
		want     []mathSpan
	}{
		{"inline", `Пусть $x_i \in \mathbb{R}$ и $a_b$.`, []mathSpan{{`x_i \in \mathbb{R}`, false}, {`a_b`, false}}},
		{"display", "Тогда\n\n$$\n\\sum_{i=1}^n x_i \\\\ y\n$$\n", []mathSpan{{"\\sum_{i=1}^n x_i \\\\ y", true}}},
		{"brackets", `\(a+b\) и \[ \frac{1}{2} \]`, []mathSpan{{`a+b`, false}, {`\frac{1}{2}`, true}}},
		{"money", `Стоит 100$ или 200$ за штуку, а \$5 — мало.`, nil},
		{"lone dollar", `Курс $ вырос`, nil},
		{"inline code", "Код `$x$` и `a $b$ c`", nil},
		{"fence", "```\n$x$ и $$y$$\n```\n", nil},
		{"table pipe", "| $|x|$ | б |\n|---|---|\n", []mathSpan{{`|x|`, false}}},
		{"empty", `$$ $$ и \( \)`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, got := protectMath(c.md)
			if len(got) != len(c.want) {
				t.Fatalf("spans = %+v, want %+v (out %q)", got, c.want, out)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("span %d = %+v, want %+v", i, got[i], c.want[i])
				}
				if !strings.Contains(out, mathToken(i)) {
					t.Errorf("no token %d in %q", i, out)
				}
			}
			if len(c.want) == 0 && out != c.md {
				t.Errorf("text changed: %q -> %q", c.md, out)
			}
		})
	}
}

func TestMarkdownHTMLMath(t *testing.T) {
	h := markdownHTML("Тема", "Дисперсия $\\sigma^2 = a_i * b_i * c$ и <b>\n\n$$\nx < y \\\\ \\frac{a}{b}\n$$\n\n| $|x|$ | б |\n|---|---|\n| 1 | 2 |\n")
	for _, want := range []string{
		`<span class="math">\sigma^2 = a_i * b_i * c</span>`,
		`<div class="math" data-display="1">x &lt; y \\ \frac{a}{b}</div>`,
		`<th><span class="math">|x|</span></th>`,
		"katex.render(",
		"data:font/woff2;base64,",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("no %q in html", want)
		}
	}
	for _, bad := range []string{"zzmath", "<em>", "url(fonts/"} {
		if strings.Contains(h, bad) {
			t.Errorf("unexpected %q in html", bad)
		}
	}
	if plain := markdownHTML("Тема", "текст за 100$"); strings.Contains(plain, "katex.render") {
		t.Error("katex must not be embedded without formulas")
	}
}

// TestWritePDFMath: формулы в PDF отрисованы KaTeX, а не напечатаны исходником.
func TestWritePDFMath(t *testing.T) {
	bin, err := exec.LookPath("chromium")
	if err != nil {
		t.Skip("no chromium for print-to-pdf")
	}
	pdf := filepath.Join(t.TempDir(), "notes.pdf")
	md := "Формула: $\\frac{a}{b} + \\sqrt{x}$\n\n$$\n\\sum_{i=1}^{n} x_i\n$$\n"
	if err := WritePDF(&config.Config{ChromeBin: bin}, "Тема", md, pdf); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(pdf)
	if err != nil {
		t.Fatal(err)
	}
	// Шрифт KaTeX в PDF есть только если формулу отрисовал KaTeX.
	if !strings.Contains(string(raw), "KaTeX_Main") {
		t.Fatal("formulas not rendered: no KaTeX font in pdf")
	}
}
