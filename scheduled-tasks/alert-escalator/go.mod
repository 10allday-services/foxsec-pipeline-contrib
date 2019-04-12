module github.com/mozilla-services/foxsec-pipeline-contrib/scheduled-tasks/alert-escalator

go 1.12

require (
	cloud.google.com/go v0.37.4 // indirect
	github.com/mozilla-services/foxsec-pipeline-contrib v0.0.0
	github.com/sirupsen/logrus v1.4.1
	go.mozilla.org/mozlogrus v1.0.0
)

replace github.com/mozilla-services/foxsec-pipeline-contrib v0.0.0 => ../../