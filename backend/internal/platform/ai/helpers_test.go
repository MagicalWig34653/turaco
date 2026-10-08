package ai_test

import (
	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai/providers/fake"
)

func scriptedFail() ai.Provider { return fake.Scripted() }
func scriptedOK() ai.Provider   { return fake.Scripted(fake.Text("OK")) }
