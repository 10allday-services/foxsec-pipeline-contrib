module github.com/mozilla-services/foxsec-pipeline-contrib/duopull

require (
	cloud.google.com/go v0.37.4
	github.com/kr/pty v1.1.3 // indirect
	github.com/mozilla-services/foxsec-pipeline-contrib v0.0.0
	github.com/mozilla-services/foxsec-pipeline-contrib/foxsec-slack-bot v0.0.0-20190422180541-854a65bd9948 // indirect
	github.com/sirupsen/logrus v1.4.2
	go.mozilla.org/mozlogrus v2.0.0+incompatible
)

replace github.com/mozilla-services/foxsec-pipeline-contrib v0.0.0 => ../

go 1.13
