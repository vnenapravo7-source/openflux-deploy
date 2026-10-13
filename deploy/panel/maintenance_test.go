package main

import "testing"

func TestLatestStableTag(t *testing.T) {
	output := "" +
		"1111111111111111111111111111111111111111\trefs/tags/v0.7.0\n" +
		"2222222222222222222222222222222222222222\trefs/tags/v0.7.1\n" +
		"3333333333333333333333333333333333333333\trefs/tags/v0.8.0-rc1\n" +
		"4444444444444444444444444444444444444444\trefs/tags/node-v1.4.0\n" +
		"5555555555555555555555555555555555555555\trefs/tags/v1.0.0\n"
	got, ok := latestStableTag(output)
	if !ok || got != "v1.0.0" {
		t.Fatalf("latestStableTag() = %q, %v; want v1.0.0, true", got, ok)
	}
}

func TestLatestStableTagSkipsNonReleases(t *testing.T) {
	if got, ok := latestStableTag("abc\trefs/tags/nightly-20261013\n"); ok || got != "" {
		t.Fatalf("latestStableTag() = %q, %v; want empty, false", got, ok)
	}
}
