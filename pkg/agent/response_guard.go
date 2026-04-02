package agent

import "strings"

var internalLeakMarkers = []string{
	"### agenda for this conversation",
	"### context and goals",
	"### steps to take",
	"### context and background",
	"### current time",
	"## current time",
	"### available tools",
	"## available tools",
	"workspace is at:",
	"internal analysis (not visible to user)",
}

var genericGreetingReplies = []string{
	"hello! how can i assist you today?",
	"hello! how can i help you today?",
	"hi! how can i assist you today?",
	"hi! how can i help you today?",
	"sorry, but i can't assist with that.",
	"sorry, but i cannot assist with that.",
}

var liveDataDenialPhrases = []string{
	"i don't have access to real-time",
	"i don't have access to real time",
	"i do not have access to real-time",
	"i do not have access to real time",
	"don't have access to real-time",
	"don't have access to real time",
	"no tengo acceso a información en tiempo real",
	"no tengo acceso a informacion en tiempo real",
	"no tengo acceso en tiempo real",
}

func responseRepairHint(userMessage, response string) string {
	lowerUser := normalizeResponseText(userMessage)
	lowerResp := normalizeResponseText(response)

	if lowerResp == "" {
		return ""
	}

	for _, marker := range internalLeakMarkers {
		if strings.Contains(lowerResp, marker) {
			return "Your previous reply leaked internal planning or system prompt content. Answer the user directly. Do not expose agenda, context blocks, tools lists, current time headers, or workspace details."
		}
	}

	for _, greeting := range genericGreetingReplies {
		if lowerResp == greeting && !isGreetingLike(lowerUser) {
			return "Your previous reply was a generic fallback greeting/refusal and did not answer the user's request. Answer the actual question directly."
		}
	}

	if isIdentityDump(lowerResp) && !isIdentityQuestion(lowerUser) {
		return "Your previous reply defaulted to an identity/introduction dump. Do not introduce yourself unless the user asks who you are. Answer the user's request directly."
	}

	if isSystemHealthQuery(lowerUser) {
		for _, phrase := range liveDataDenialPhrases {
			if strings.Contains(lowerResp, phrase) {
				return "The user asked for live system health. Read the sentinel state file and answer with the current metrics. Do not claim you lack access to real-time hardware data."
			}
		}
	}

	return ""
}

func shouldPersistAssistantResponse(userMessage, response, defaultResponse string) bool {
	trimmed := strings.TrimSpace(response)
	if trimmed == "" {
		return false
	}
	if defaultResponse != "" && trimmed == strings.TrimSpace(defaultResponse) {
		return false
	}
	return responseRepairHint(userMessage, response) == ""
}

func normalizeResponseText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Join(strings.Fields(s), " ")
	return s
}

func isGreetingLike(s string) bool {
	if s == "" {
		return false
	}
	greetingPrefixes := []string{
		"hola", "hello", "hi", "buenas", "buen dia", "buen día",
		"buenas tardes", "buenas noches", "que onda", "qué onda",
		"como va", "cómo va", "todo bien", "buen día chango", "hola chango",
	}
	for _, prefix := range greetingPrefixes {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return len(strings.Fields(s)) <= 4
}

func isSystemHealthQuery(s string) bool {
	healthHints := []string{
		"cpu", "temperatura", "temperatura de la cpu", "temp", "ram",
		"memoria", "disk", "disco", "hardware", "healthcheck", "system health",
	}
	for _, hint := range healthHints {
		if strings.Contains(s, hint) {
			return true
		}
	}
	return false
}

func isIdentityQuestion(s string) bool {
	identityHints := []string{
		"quien sos", "quién sos", "que sos", "qué sos", "who are you",
		"what are you", "who is chango", "what can you do", "qué podés hacer",
		"que podes hacer",
	}
	for _, hint := range identityHints {
		if strings.Contains(s, hint) {
			return true
		}
	}
	return false
}

func isIdentityDump(s string) bool {
	identityMarkers := []string{
		"i am chango", "soy chango", "autonomous ai agent",
		"created by diego", "running on a raspberry pi 5",
		"my capabilities are defined", "identity.md", "agents.md",
	}
	matches := 0
	for _, marker := range identityMarkers {
		if strings.Contains(s, marker) {
			matches++
		}
	}
	return matches >= 2
}
