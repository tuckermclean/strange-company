package tofustate_test

import (
	"context"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/store"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/tofustate"
)

// 06-AC-1 (plan 06 sections 14.3 and 15): a 404 NoSuchBucket or a 400 AuthorizationHeaderMalformed answer to the
// conditional create says nothing about If-None-Match support. It is fatal but must not be reported as
// S3_CONDITIONAL_PUT_UNSUPPORTED (C1) or PF_S3_LOCK_UNSUPPORTED (C2/C3). Only an ignored header, a 501 or a 400
// NotImplemented/InvalidArgument/InvalidRequest counts as unsupported.
func TestConformance_NoSuchBucketAndMalformedAuthAreNotUnsupported_06AC1(t *testing.T) {
	faults := []named[error]{
		{"404 NoSuchBucket", &s3client.Error{Status: 404, Code: "NoSuchBucket", Message: "no such bucket", Kind: store.NotFound}},
		{"400 AuthorizationHeaderMalformed", &s3client.Error{Status: 400, Code: "AuthorizationHeaderMalformed", Message: "bad region"}},
	}
	steps := []named[func(key string, call int) bool]{
		{"C1 first create", func(k string, c int) bool { return strings.HasSuffix(k, "/k1") && c == 1 }},
		{"C2 second create", func(k string, c int) bool { return strings.HasSuffix(k, "/k1") && c == 2 }},
		{"C3 concurrent creates", func(k string, c int) bool { return strings.HasSuffix(k, "/k2") }},
	}
	for _, fl := range faults {
		for _, st := range steps {
			fault, step := fl.v, st.v
			t.Run(st.name+"/"+fl.name, func(t *testing.T) {
				m := newMemS3()
				fs := newFaultStore(m, func(k string, c int) error { return when(step(k, c), fault) })
				rep, err := tofustate.Conformance(context.Background(), tofustate.ConformanceParams{Store: fs, Cluster: cluster, RunID: runID, Clock: m.clk})
				if err == nil && len(rep.Fatal) == 0 {
					t.Errorf("the failure was swallowed: no error and fatal = %v", rep.Fatal)
				}
				for _, code := range []string{"S3_CONDITIONAL_PUT_UNSUPPORTED", "PF_S3_LOCK_UNSUPPORTED"} {
					if hasFatal(rep, code) {
						t.Errorf("fatal %v reports %s for %s", rep.Fatal, code, fl.name)
					}
					for _, r := range rep.Tests {
						if r.Code == code {
							t.Errorf("row %s carries %s for %s", r.ID, code, fl.name)
						}
					}
				}
			})
		}
	}
}

// 06-AC-1: the other direction. A 501, or a 400 with NotImplemented, InvalidArgument or InvalidRequest, in answer
// to the conditional header is still "unsupported".
func TestConformance_NotImplementedAnswersAreStillUnsupported_06AC1(t *testing.T) {
	faults := []named[error]{
		{"501", &s3client.Error{Status: 501, Code: "NotImplemented", Kind: store.Unsupported}},
		{"400 NotImplemented", &s3client.Error{Status: 400, Code: "NotImplemented", Message: "header not implemented"}},
		{"400 InvalidArgument", &s3client.Error{Status: 400, Code: "InvalidArgument", Message: "If-None-Match"}},
		{"400 InvalidRequest", &s3client.Error{Status: 400, Code: "InvalidRequest", Message: "If-None-Match"}},
	}
	steps := []named[struct {
		when func(key string, call int) bool
		code string
	}]{
		{"C1", struct {
			when func(key string, call int) bool
			code string
		}{func(k string, c int) bool { return strings.HasSuffix(k, "/k1") && c == 1 }, "S3_CONDITIONAL_PUT_UNSUPPORTED"}},
		{"C2", struct {
			when func(key string, call int) bool
			code string
		}{func(k string, c int) bool { return strings.HasSuffix(k, "/k1") && c == 2 }, "PF_S3_LOCK_UNSUPPORTED"}},
		{"C3", struct {
			when func(key string, call int) bool
			code string
		}{func(k string, c int) bool { return strings.HasSuffix(k, "/k2") }, "PF_S3_LOCK_UNSUPPORTED"}},
	}
	for _, fl := range faults {
		for _, st := range steps {
			fault, step := fl.v, st.v
			t.Run(st.name+"/"+fl.name, func(t *testing.T) {
				m := newMemS3()
				fs := newFaultStore(m, func(k string, c int) error { return when(step.when(k, c), fault) })
				rep, _ := tofustate.Conformance(context.Background(), tofustate.ConformanceParams{Store: fs, Cluster: cluster, RunID: runID, Clock: m.clk})
				if !hasFatal(rep, step.code) {
					t.Errorf("fatal = %v, want %s", rep.Fatal, step.code)
				}
			})
		}
	}
}
