package tools

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// tfidfIndex provides TF-IDF based semantic search over vault notes.
type tfidfIndex struct {
	docFreq    map[string]int            // term -> number of documents containing term
	docVectors map[string]map[string]int // docKey -> term -> count
	docCount   int
}

func newTFIDFIndex() *tfidfIndex {
	return &tfidfIndex{
		docFreq:    make(map[string]int),
		docVectors: make(map[string]map[string]int),
	}
}

// addDocument indexes a document by key with the given text.
func (idx *tfidfIndex) addDocument(key, text string) {
	// Remove old version first if exists
	idx.removeDocument(key)

	tokens := tokenize(text)
	termCounts := make(map[string]int)
	for _, t := range tokens {
		termCounts[t]++
	}

	idx.docVectors[key] = termCounts
	idx.docCount++

	// Update document frequencies
	for term := range termCounts {
		idx.docFreq[term]++
	}
}

// removeDocument removes a document from the index.
func (idx *tfidfIndex) removeDocument(key string) {
	tc, exists := idx.docVectors[key]
	if !exists {
		return
	}

	for term := range tc {
		idx.docFreq[term]--
		if idx.docFreq[term] <= 0 {
			delete(idx.docFreq, term)
		}
	}
	delete(idx.docVectors, key)
	idx.docCount--
}

type searchResult struct {
	Key   string
	Score float64
}

// search returns top-N documents ranked by TF-IDF cosine similarity.
func (idx *tfidfIndex) search(query string, n int) []searchResult {
	if idx.docCount == 0 {
		return nil
	}

	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		return nil
	}

	// Build query TF vector
	queryTF := make(map[string]int)
	for _, t := range queryTokens {
		queryTF[t]++
	}

	// Compute query TF-IDF vector
	queryVec := make(map[string]float64)
	for term, tf := range queryTF {
		df := idx.docFreq[term]
		if df == 0 {
			continue
		}
		idf := math.Log(float64(idx.docCount+1) / float64(df+1))
		queryVec[term] = float64(tf) * idf
	}

	if len(queryVec) == 0 {
		return nil
	}

	// Score each document
	var results []searchResult
	for key, docTC := range idx.docVectors {
		// Compute doc TF-IDF and cosine similarity with query
		var dot, docNorm float64
		for term, tf := range docTC {
			df := idx.docFreq[term]
			if df == 0 {
				continue
			}
			idf := math.Log(float64(idx.docCount+1) / float64(df+1))
			tfidf := float64(tf) * idf
			docNorm += tfidf * tfidf
			if qv, ok := queryVec[term]; ok {
				dot += tfidf * qv
			}
		}

		if dot <= 0 {
			continue
		}

		// Query norm
		var qNorm float64
		for _, v := range queryVec {
			qNorm += v * v
		}

		score := dot / (math.Sqrt(docNorm) * math.Sqrt(qNorm))
		results = append(results, searchResult{Key: key, Score: score})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if n > 0 && len(results) > n {
		results = results[:n]
	}

	return results
}

// tokenize splits text into lowercase tokens, filtering stop words.
func tokenize(text string) []string {
	text = strings.ToLower(text)
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	var tokens []string
	for _, w := range words {
		if len(w) < 2 || stopWords[w] {
			continue
		}
		tokens = append(tokens, w)
	}
	return tokens
}

// stopWords contains common Spanish and English stop words.
var stopWords = map[string]bool{
	// Spanish
	"de": true, "la": true, "el": true, "en": true, "un": true, "una": true,
	"los": true, "las": true, "del": true, "al": true, "es": true, "lo": true,
	"que": true, "se": true, "no": true, "por": true, "con": true, "para": true,
	"su": true, "ya": true, "si": true, "como": true, "mas": true, "pero": true,
	"sus": true, "les": true, "nos": true, "muy": true, "sin": true, "sobre": true,
	"ser": true, "tiene": true, "hay": true, "este": true, "esta": true, "eso": true,
	"son": true, "fue": true, "todo": true, "cada": true, "entre": true,
	"desde": true, "hasta": true, "donde": true, "quien": true, "otro": true, "otra": true,
	"cuando": true, "tanto": true, "porque": true, "tambien": true, "puede": true,
	// English
	"the": true, "be": true, "to": true, "of": true, "and": true, "in": true,
	"that": true, "have": true, "it": true, "for": true, "not": true, "on": true,
	"with": true, "he": true, "as": true, "you": true, "do": true, "at": true,
	"this": true, "but": true, "his": true, "by": true, "from": true, "they": true,
	"we": true, "say": true, "her": true, "she": true, "or": true, "an": true,
	"will": true, "my": true, "all": true, "would": true, "there": true, "their": true,
	"what": true, "so": true, "up": true, "out": true, "if": true, "about": true,
	"who": true, "get": true, "which": true, "go": true, "me": true, "when": true,
	"make": true, "can": true, "like": true, "time": true, "just": true, "him": true,
	"know": true, "take": true, "people": true, "into": true, "year": true, "your": true,
	"some": true, "could": true, "them": true, "than": true, "then": true, "now": true,
	"its": true, "also": true, "after": true, "was": true, "were": true, "been": true,
	"has": true, "had": true, "are": true, "is": true, "am": true,
}
