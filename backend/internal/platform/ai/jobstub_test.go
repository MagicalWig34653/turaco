package ai_test

import "github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"

func jobStub(id string) jobs.Job { return jobs.Job{ID: id} }
