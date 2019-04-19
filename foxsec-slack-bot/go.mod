module github.com/mozilla-services/foxsec-pipeline-contrib/foxsec-slack-bot

require (
	github.com/gorilla/websocket v1.4.0 // indirect
	github.com/lusis/go-slackbot v0.0.0-20180109053408-401027ccfef5 // indirect
	github.com/lusis/slack-test v0.0.0-20190408224659-6cf59653add2 // indirect
	github.com/mozilla-services/foxsec-pipeline-contrib v0.0.0
	github.com/nlopes/slack v0.5.0
	github.com/pkg/errors v0.8.1 // indirect
	github.com/sirupsen/logrus v1.3.0
	go.mozilla.org/mozlog v0.0.0-20170222151521-4bb13139d403 // indirect
	go.mozilla.org/mozlogrus v1.0.0
)

replace github.com/mozilla-services/foxsec-pipeline-contrib v0.0.0 => ../
