package notes

import (
	"embed"
	"encoding/base64"
	"fmt"
	"html"
	"regexp"
	"strings"
	"sync"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// KaTeX вшит в бинарник: PDF печатается на VDS без похода в CDN, а шрифты
// едут data:-ссылками — file:// из другой папки Chromium шрифтам не отдаёт.
//
//go:embed katex/katex.min.js katex/katex.min.css katex/fonts/*.woff2
var katexFS embed.FS

type mathSpan struct {
	tex     string
	display bool
}

// maxInlineMath: «$» без пары не должен съесть абзац до следующего «$».
const maxInlineMath = 600

func mathToken(i int) string { return fmt.Sprintf("zzmath%dzz", i) }

// protectMath вырезает LaTeX-формулы из Markdown и ставит на их место
// токены: иначе goldmark съедает «\\», «\(» и превращает «_» в курсив.
// Понимает $$…$$, \[…\], \(…\) и $…$; код (``` и `…`) не трогает.
func protectMath(md string) (string, []mathSpan) {
	var spans []mathSpan
	var out, chunk strings.Builder
	flush := func() {
		out.WriteString(protectChunk(chunk.String(), &spans))
		chunk.Reset()
	}
	fence := ""
	for _, line := range strings.SplitAfter(md, "\n") {
		t := strings.TrimLeft(line, " ")
		if fence == "" {
			if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
				flush()
				fence = t[:3]
				out.WriteString(line)
				continue
			}
			chunk.WriteString(line)
			continue
		}
		out.WriteString(line)
		if strings.HasPrefix(t, fence) {
			fence = ""
		}
	}
	flush()
	return out.String(), spans
}

func protectChunk(s string, spans *[]mathSpan) string {
	var b strings.Builder
	add := func(tex string, display bool) {
		b.WriteString(mathToken(len(*spans)))
		*spans = append(*spans, mathSpan{tex: strings.TrimSpace(tex), display: display})
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '`':
			n := 1
			for i+n < len(s) && s[i+n] == '`' {
				n++
			}
			run := s[i : i+n]
			if end := strings.Index(s[i+n:], run); end >= 0 {
				b.WriteString(s[i : i+n+end+n])
				i += n + end + n
			} else {
				b.WriteString(run)
				i += n
			}
		case c == '\\' && i+1 < len(s) && (s[i+1] == '[' || s[i+1] == '('):
			closer := `\]`
			if s[i+1] == '(' {
				closer = `\)`
			}
			end := strings.Index(s[i+2:], closer)
			if end < 0 || strings.TrimSpace(s[i+2:i+2+end]) == "" {
				b.WriteString(s[i : i+2])
				i += 2
				continue
			}
			add(s[i+2:i+2+end], s[i+1] == '[')
			i += 2 + end + 2
		case c == '\\' && i+1 < len(s):
			// «\$» — это знак доллара, а не начало формулы.
			b.WriteString(s[i : i+2])
			i += 2
		case c == '$' && strings.HasPrefix(s[i:], "$$"):
			end := strings.Index(s[i+2:], "$$")
			if end < 0 || strings.TrimSpace(s[i+2:i+2+end]) == "" {
				b.WriteString("$$")
				i += 2
				continue
			}
			add(s[i+2:i+2+end], true)
			i += 2 + end + 2
		case c == '$':
			end := inlineMathEnd(s[i+1:])
			if end < 0 {
				b.WriteByte(c)
				i++
				continue
			}
			add(s[i+1:i+1+end], false)
			i += 1 + end + 1
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// inlineMathEnd ищет закрывающий «$» по правилам pandoc: формула не начинается
// и не кончается пробелом, после неё не идёт цифра — «100$ и 200$» остаются текстом.
func inlineMathEnd(s string) int {
	if s == "" || s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '$' {
		return -1
	}
	for j := 0; j < len(s) && j <= maxInlineMath; j++ {
		switch s[j] {
		case '\\':
			j++
		case '\n':
			if j+1 < len(s) && s[j+1] == '\n' {
				return -1
			}
		case '$':
			prev := s[j-1]
			if prev == ' ' || prev == '\t' || prev == '\n' {
				continue
			}
			if j+1 < len(s) && s[j+1] >= '0' && s[j+1] <= '9' {
				continue
			}
			return j
		}
	}
	return -1
}

// restoreMath возвращает формулы в готовый HTML — уже экранированными и в
// элементах, которые KaTeX отрисует на странице.
func restoreMath(page string, spans []mathSpan) string {
	if len(spans) == 0 {
		return page
	}
	pairs := make([]string, 0, len(spans)*4)
	for i := range spans {
		sp := spans[i]
		tex := html.EscapeString(sp.tex)
		if sp.display {
			tok := mathToken(i)
			block := `<div class="math" data-display="1">` + tex + `</div>`
			// Формула отдельным абзацем — блок, а не <div> внутри <p>.
			pairs = append(pairs, "<p>"+tok+"</p>", block)
			pairs = append(pairs, tok, `<span class="math" data-display="1">`+tex+`</span>`)
			continue
		}
		pairs = append(pairs, mathToken(i), `<span class="math">`+tex+`</span>`)
	}
	return strings.NewReplacer(pairs...).Replace(page)
}

var (
	katexOnce  sync.Once
	katexHead  string
	katexFoot  string
	katexFontR = regexp.MustCompile(`url\(fonts/([A-Za-z0-9_-]+\.woff2)\) format\("woff2"\)(,url\(fonts/[A-Za-z0-9_-]+\.(woff|ttf)\) format\("[a-z]+"\))*`)
)

// katexAssets отдаёт <style> для <head> и <script> для конца <body>.
func katexAssets() (head, foot string) {
	katexOnce.Do(func() {
		css, err := katexFS.ReadFile("katex/katex.min.css")
		if err != nil {
			logx.Errorf("notes", "katexAssets: css: %v", err)
			return
		}
		js, err := katexFS.ReadFile("katex/katex.min.js")
		if err != nil {
			logx.Errorf("notes", "katexAssets: js: %v", err)
			return
		}
		inlined := katexFontR.ReplaceAllStringFunc(string(css), func(m string) string {
			name := katexFontR.FindStringSubmatch(m)[1]
			font, err := katexFS.ReadFile("katex/fonts/" + name)
			if err != nil {
				logx.Warnf("notes", "katexAssets: font %s: %v", name, err)
				return m
			}
			return `url(data:font/woff2;base64,` + base64.StdEncoding.EncodeToString(font) + `) format("woff2")`
		})
		katexHead = "<style>" + inlined + "</style>\n"
		katexFoot = "<script>" + string(js) + "</script>\n" + `<script>
document.querySelectorAll('.math').forEach(function (el) {
  var tex = el.textContent;
  try {
    katex.render(tex, el, {displayMode: el.dataset.display === '1', throwOnError: false, strict: false});
  } catch (e) {
    el.textContent = tex;
  }
});
</script>
`
	})
	return katexHead, katexFoot
}
