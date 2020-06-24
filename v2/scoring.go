// Copyright 2020 Google Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package classifier

import (
	"strconv"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// return values for the distance function that explain why a diff
// is not an acceptable match for the source document.
const (
	versionChange          = -1
	introducedPhraseChange = -2
	lesserGPLChange        = -3
)

// score computes a metric of similarity between the known and unknown
// document, including the offsets into the unknown that yield the content
// generating the computed similarity.
func score(id string, unknown, known *indexedDocument, unknownStart, unknownEnd int) (float64, int, int) {
	if traceScoring(known.s.origin) {
		Trace("Scoring %s: [%d-%d]\n", known.s.origin, unknownStart, unknownEnd)
	}

	knownLength := len(known.Tokens)
	diffs := docDiff(id, unknown, unknownStart, unknownEnd, known, 0, len(known.Tokens))

	start, end := diffRange(known.normalized(), diffs)
	distance := scoreDiffs(diffs[start:end])
	if distance < 0 {
		// If the distance is negative, this indicates an unacceptable diff so we return a zero-confidence match.
		if traceScoring(known.s.origin) {
			Trace("Distance result %v, rejected match", distance)
		}
		return 0.0, 0, 0
	}

	// Applying the diffRange-generated offsets provides the run of text from the
	// target most closely correlated to the source.  This is the process for
	// compensating for errors from the searchset. With the reduced text, we
	// compute the final confidence score and exact text locations for the
	// matched content.
	// The diff slice consists of three regions: an optional deletion diff for
	// target text before the source, n elements that represent the diff between
	// the source and target, and an optional deletion diff for text after the
	// target.  The helper functions identify the portions of the slice
	// corresponding to those regions.  This results in a more accurate
	// confidence score and better position detection of the source in the
	// target.
	conf, so, eo := confidencePercentage(knownLength, distance), textLength(diffs[:start]), textLength(diffs[end:])

	if traceScoring(known.s.origin) {
		Trace("Score result: %v [%d-%d]\n", conf, so, eo)
	}
	return conf, so, eo
}

// confidencePercentage computes a confidence match score for the lengths,
// handling the cases where source and target lengths differ.
func confidencePercentage(klen, distance int) float64 {
	// No text is matched at 100% confidence (avoid divide by zero).
	if klen == 0 {
		return 1.0
	}

	// Return a computed fractional match against the known text.
	return 1.0 - float64(distance)/float64(klen)
}

// diffLevenshteinWord computes word-based Levenshtein count.
func diffLevenshteinWord(diffs []diffmatchpatch.Diff) int {
	levenshtein := 0
	insertions := 0
	deletions := 0

	for _, aDiff := range diffs {
		switch aDiff.Type {
		case diffmatchpatch.DiffInsert:
			insertions += wordLen(aDiff.Text)
		case diffmatchpatch.DiffDelete:
			deletions += wordLen(aDiff.Text)
		case diffmatchpatch.DiffEqual:
			// A deletion and an insertion is one substitution.
			levenshtein += max(insertions, deletions)
			insertions = 0
			deletions = 0
		}
	}

	levenshtein += max(insertions, deletions)
	return levenshtein
}

// scoreDiffs returns a score rating the acceptability of these diffs.  A
// negative value means that the changes represented by the diff are not an
// acceptable transformation since it would change the underlying license.  A
// positive value indicates the Levenshtein word distance.
func scoreDiffs(diffs []diffmatchpatch.Diff) int {
	// We make a pass looking for unacceptable substitutions
	// Delete diffs are always ordered before insert diffs. This is leveraged to
	// analyze a change by checking an insert against the delete text that was
	// previously cached.
	prevText := ""
	prevDelete := ""
	for _, diff := range diffs {
		text := strings.TrimSpace(diff.Text)
		switch diff.Type {
		case diffmatchpatch.DiffInsert:
			num := text
			if i := strings.Index(num, " "); i != -1 {
				num = num[0:i]
			}
			if _, err := strconv.ParseFloat(num, 32); err == nil && strings.HasSuffix(prevText, "version") {
				if !strings.HasSuffix(prevText, "the standard version") && !strings.HasSuffix(prevText, "the contributor version") {
					return versionChange
				}
			}
			// There are certain phrases that can't be introduced to make a license
			// hit.  TODO: would like to generate this programmatically. Most of
			// these are words or phrases that appear in a single/small number of
			// licenses. Can we leverage frequency analysis to identify these
			// interesting words/phrases and auto-extract them?
			for _, p := range []string{"autoconf exception", "class path exception", "gcc linking exception", "bison exception", "font exception", "imagemagick", "x consortium", "apache", "bsd", "affero", "sun standards"} {
				if strings.Index(text, p) != -1 {
					return introducedPhraseChange
				}
			}

			// Ignore changes between "library" and "lesser" in a GNU context as they
			// changed the terms, but look for introductions of Lesser that would
			// otherwise disqualify a match.
			if text == "lesser" && strings.HasSuffix(prevText, "gnu") && prevDelete != "library" {
				// The LGPL 3.0 doesn't have a standard header, so people tend to craft
				// their own. As a result, sometimes the warranty clause refers to the
				// GPL instead of the LGPL. This is fine from a licensing perspective,
				// but we need to tweak matching to ignore that particular case. In
				// other circumstances, inserting or removing the word Lesser in the
				// GPL context is not an acceptable change.
				if !strings.Contains(prevText, "warranty") {
					return lesserGPLChange
				}
			}
		case diffmatchpatch.DiffEqual:
			prevText = text
			prevDelete = ""

		case diffmatchpatch.DiffDelete:
			if text == "lesser" && strings.HasSuffix(prevText, "gnu") {
				// Same as above to avoid matching GPL instead of LGPL here.
				if !strings.Contains(prevText, "warranty") {
					return lesserGPLChange
				}
			}
			prevDelete = text
		}
	}
	return diffLevenshteinWord(diffs)
}