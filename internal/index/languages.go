package index

import (
	"path/filepath"
	"strings"
)

// Language is a stable host-language identifier. Recognition controls result
// metadata and filters only; it does not imply syntax-aware chunking or a
// structural backend. Unhandled languages always use the generic chunker.
type Language string

const (
	LangGo          Language = "go"
	LangPython      Language = "python"
	LangJavaScript  Language = "javascript"
	LangTypeScript  Language = "typescript"
	LangVue         Language = "vue"
	LangRust        Language = "rust"
	LangJava        Language = "java"
	LangKotlin      Language = "kotlin"
	LangScala       Language = "scala"
	LangC           Language = "c"
	LangCPP         Language = "cpp"
	LangCUDA        Language = "cuda"
	LangCSharp      Language = "csharp"
	LangVisualBasic Language = "visualbasic"
	LangRuby        Language = "ruby"
	LangPHP         Language = "php"
	LangDart        Language = "dart"
	LangSwift       Language = "swift"
	LangLua         Language = "lua"
	LangElixir      Language = "elixir"
	LangSvelte      Language = "svelte"
	LangAstro       Language = "astro"
	LangRazor       Language = "razor"
	LangShell       Language = "shell"
	LangHCL         Language = "hcl"
	LangTerraform   Language = "terraform"
	LangSQL         Language = "sql"
	LangMarkdown    Language = "markdown"
	LangJSON        Language = "json"
	LangYAML        Language = "yaml"
	LangTOML        Language = "toml"
	LangHTML        Language = "html"
	LangCSS         Language = "css"
	LangUnknown     Language = "unknown"
)

var compoundLanguageSuffixes = []struct {
	suffix   string
	language Language
}{
	{suffix: ".tfvars.json", language: LangTerraform},
	{suffix: ".tf.json", language: LangTerraform},
}

// languageExtensions is a recognition catalog, not a structural-support
// registry. Keep it aligned with codemap's host-language identifiers.
var languageExtensions = map[string]Language{
	".go": LangGo,

	".py":  LangPython,
	".pyw": LangPython,
	".pyi": LangPython,

	".js":  LangJavaScript,
	".mjs": LangJavaScript,
	".cjs": LangJavaScript,
	".jsx": LangJavaScript,
	".ts":  LangTypeScript,
	".tsx": LangTypeScript,
	".mts": LangTypeScript,
	".cts": LangTypeScript,
	".vue": LangVue,

	".rs":    LangRust,
	".java":  LangJava,
	".kt":    LangKotlin,
	".kts":   LangKotlin,
	".scala": LangScala,

	".c":   LangC,
	".h":   LangC,
	".cpp": LangCPP,
	".cc":  LangCPP,
	".cxx": LangCPP,
	".c++": LangCPP,
	".hh":  LangCPP,
	".hpp": LangCPP,
	".hxx": LangCPP,
	".ipp": LangCPP,
	".tpp": LangCPP,
	".cu":  LangCUDA,
	".cuh": LangCUDA,

	".cs":  LangCSharp,
	".csx": LangCSharp,
	".vb":  LangVisualBasic,

	".rb":    LangRuby,
	".php":   LangPHP,
	".phtml": LangPHP,
	".dart":  LangDart,
	".swift": LangSwift,
	".lua":   LangLua,
	".ex":    LangElixir,
	".exs":   LangElixir,

	".svelte": LangSvelte,
	".astro":  LangAstro,
	".razor":  LangRazor,
	".cshtml": LangRazor,
	".vbhtml": LangRazor,

	".sh":   LangShell,
	".bash": LangShell,
	".zsh":  LangShell,
	".ksh":  LangShell,
	".fish": LangShell,

	".hcl":    LangHCL,
	".tf":     LangTerraform,
	".tfvars": LangTerraform,
	".sql":    LangSQL,

	".md":   LangMarkdown,
	".json": LangJSON,
	".yaml": LangYAML,
	".yml":  LangYAML,
	".toml": LangTOML,
	".html": LangHTML,
	".htm":  LangHTML,
	".css":  LangCSS,
	".scss": LangCSS,
	".sass": LangCSS,
	".less": LangCSS,
}

// DetectLanguage maps a path to its host language. Compound suffixes are
// checked before filepath.Ext so Terraform JSON variants are not mislabeled
// as ordinary JSON.
func DetectLanguage(filename string) Language {
	lower := strings.ToLower(filepath.ToSlash(filename))
	for _, candidate := range compoundLanguageSuffixes {
		if strings.HasSuffix(lower, candidate.suffix) {
			return candidate.language
		}
	}
	if lang, ok := languageExtensions[strings.ToLower(filepath.Ext(lower))]; ok {
		return lang
	}

	base := strings.ToLower(filepath.Base(lower))
	switch {
	case base == "makefile" || base == "gnumakefile":
		return LangShell
	case base == "dockerfile" || strings.HasPrefix(base, "dockerfile."):
		return LangShell
	case base == "jenkinsfile":
		return LangShell
	case strings.HasSuffix(base, "rc") && !strings.Contains(base, "."):
		return LangShell
	default:
		return LangUnknown
	}
}
