package local

import (
	"errors"
	"strings"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Skill"
)

func TestSkillDocumentValidation(t *testing.T) {
	valid := skillDocument("valid-skill", "A useful description explaining when this Skill applies.", "# Steps\nDo the work.")
	metadata, err := parseSkillDocument([]byte(valid))
	if err != nil || metadata.Name != "valid-skill" || !strings.Contains(valid, "# Steps") {
		t.Fatalf("metadata=%+v err=%v", metadata, err)
	}
	invalid := []string{
		"no frontmatter",
		"---\nname: invalid\ndescription: missing close",
		"---\nname: Invalid\ndescription: uppercase name\n---\n",
		"---\nname: double--hyphen\ndescription: invalid name\n---\n",
		"---\nname: missing-description\n---\n",
		"---\nname: unknown-field\ndescription: valid description\nunexpected: true\n---\n",
	}
	for _, document := range invalid {
		if _, err := parseSkillDocument([]byte(document)); !errors.Is(err, skill.ErrInvalidSkill) {
			t.Errorf("document %q error = %v", document, err)
		}
	}
}
