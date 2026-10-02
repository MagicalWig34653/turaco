package application

import "github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"

// NotificationCategories are the notification categories the Tasks module
// creates (see Consumers); the composition roots register them.
func NotificationCategories() []notifications.Category {
	return []notifications.Category{
		{
			Name: "task.assigned", Owner: "tasks", LinkType: "task", LinkPath: "/tasks/{id}",
			Email: map[string]notifications.EmailText{
				"en": {Subject: "Task assigned: %s", Intro: "A task was assigned to you:", Action: "Open task"},
				"de": {Subject: "Aufgabe zugewiesen: %s", Intro: "Dir wurde eine Aufgabe zugewiesen:", Action: "Aufgabe öffnen"},
			},
		},
		{
			Name: "task.completed", Owner: "tasks", LinkType: "task", LinkPath: "/tasks/{id}",
			Email: map[string]notifications.EmailText{
				"en": {Subject: "Task completed: %s", Intro: "A task you created was completed:", Action: "Open task"},
				"de": {Subject: "Aufgabe erledigt: %s", Intro: "Eine von dir angelegte Aufgabe wurde erledigt:", Action: "Aufgabe öffnen"},
			},
		},
	}
}
