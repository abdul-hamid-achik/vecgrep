package index

import "testing"

func TestDetectLanguageRecognitionCatalog(t *testing.T) {
	tests := map[string]Language{
		"component.vue":           LangVue,
		"component.svelte":        LangSvelte,
		"page.astro":              LangAstro,
		"main.tf":                 LangTerraform,
		"prod.tfvars.json":        LangTerraform,
		"module.tf.json":          LangTerraform,
		"script.csx":              LangCSharp,
		"kernel.cu":               LangCUDA,
		"view.cshtml":             LangRazor,
		"types.pyi":               LangPython,
		"module.cts":              LangTypeScript,
		"Dockerfile.production":   LangShell,
		"nested/path/Jenkinsfile": LangShell,
	}
	for path, want := range tests {
		if got := DetectLanguage(path); got != want {
			t.Errorf("DetectLanguage(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestRecognizedLanguageWithoutParserUsesGenericFallback(t *testing.T) {
	const content = "resource \"example\" \"main\" {\n  value = true\n}"
	chunks := NewChunker(DefaultChunkerConfig()).ChunkFile(content, "main.tf")
	if len(chunks) != 1 || chunks[0].Content != content || chunks[0].ChunkType != ChunkTypeGeneric {
		t.Fatalf("Terraform fallback chunks = %+v", chunks)
	}
	if got := DetectLanguage("main.tf"); got != LangTerraform {
		t.Fatalf("host language = %q", got)
	}
}
