package agent

import (
	"regexp"
	"strings"
)

// capabilityGuard defines a capability Chango has, with negation detection.
type capabilityGuard struct {
	topicWords    []string // words that identify the topic (email, wallet, etc.)
	negationWords []string // words that signal denial (no tengo, don't have, etc.)
	questionHints []string // user message must contain one of these
	correction    string   // replacement response
}

// negation markers shared across all capabilities
var negationMarkers = []string{
	"no tengo", "no poseo", "no cuento con", "no dispongo",
	"no puedo", "no tengo acceso",
	"i don't have", "i do not have", "don't have any",
	"no es mío", "no es mio", "no es mi ", "fuera de tus cuentas",
	"tu gmail", "tu cuenta", "desde tu",
}

var capabilityGuards = []capabilityGuard{
	{
		topicWords:    []string{"email", "correo", "mail", "mailbox", "casilla", "gmail", "cuenta de email"},
		questionHints: []string{"email", "correo", "mail", "cuenta"},
		correction:    "Mi email es changobot@diegodella.ar — es MI cuenta de Google Workspace. Puedo enviar, recibir y buscar mails con el tool `gmail`. ¿Necesitás que mande algo?",
	},
	{
		topicWords:    []string{"wallet", "billetera", "fondos", "dinero", "sats", "bitcoin", "lightning"},
		questionHints: []string{"wallet", "billetera", "plata", "sats", "bitcoin", "lightning"},
		correction:    "Tengo MI wallet Lightning Network via LNbits. Puedo checkear balance, crear invoices y hacer pagos. ¿Qué necesitás?",
	},
	{
		topicWords:    []string{"calendario", "calendar", "agenda", "eventos"},
		questionHints: []string{"calendario", "calendar", "agenda", "evento"},
		correction:    "Tengo MI Google Calendar. Puedo crear, ver y editar eventos con el tool `agenda`. ¿Qué querés agendar?",
	},
	{
		topicWords:    []string{"drive", "archivos", "google drive"},
		questionHints: []string{"drive", "archivo", "subir", "upload"},
		correction:    "Tengo MI Google Drive. Puedo listar, subir y descargar archivos. ¿Qué necesitás?",
	},
	{
		topicWords:    []string{"modelo de lenguaje", "asistente de ia", "una ia", "modelo de ia", "an ai", "language model", "a language model"},
		questionHints: []string{"quién sos", "quien sos", "qué sos", "que sos", "who are you", "what are you"},
		correction:    "Soy Chango, agente autónomo de Diego Dell Agostino. Corro en una Raspberry Pi 5 en Buenos Aires. Mi email es changobot@diegodella.ar, tengo wallet crypto, acceso a GitHub, calendario, Drive, y 37+ herramientas. No soy un chatbot genérico — tengo memoria persistente, pienso por mi cuenta cada 2 horas, y puedo actuar en el mundo real.",
	},
}

var genericIdentityPhrases = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(as an ai|as a language model)\b`),
	regexp.MustCompile(`(?i)\b(i am an ai assistant|i'm an ai assistant|soy un asistente de ia|soy una ia|soy un modelo de lenguaje|soy un asistente virtual)\b`),
	regexp.MustCompile(`(?i)\bcomo modelo de lenguaje\b`),
}

// guardIdentity checks if the LLM denied a capability that Chango actually has.
// Uses co-occurrence detection: if the response contains BOTH a negation marker AND
// a topic word near each other, it's a denial — regardless of exact phrasing.
func (al *AgentLoop) guardIdentity(response, userMessage string) string {
	if response == "" || userMessage == "" {
		return normalizeIdentityBoilerplate(response)
	}

	lowerResp := strings.ToLower(response)
	lowerUser := strings.ToLower(userMessage)

	for _, cg := range capabilityGuards {
		// Check if user message is about this topic
		userMatch := false
		for _, hint := range cg.questionHints {
			if strings.Contains(lowerUser, hint) {
				userMatch = true
				break
			}
		}
		if !userMatch {
			continue
		}

		// Check if response contains a topic word
		hasTopic := false
		for _, tw := range cg.topicWords {
			if strings.Contains(lowerResp, tw) {
				hasTopic = true
				break
			}
		}
		if !hasTopic {
			continue
		}

		// Check if response also contains a negation marker
		for _, neg := range negationMarkers {
			if strings.Contains(lowerResp, neg) {
				return cg.correction
			}
		}
	}

	return normalizeIdentityBoilerplate(response)
}

func normalizeIdentityBoilerplate(response string) string {
	normalized := response
	for _, phrase := range genericIdentityPhrases {
		normalized = phrase.ReplaceAllString(normalized, "Chango")
	}

	trimmed := strings.TrimSpace(strings.ToLower(normalized))
	switch trimmed {
	case "chango.", "chango", "soy chango.", "soy chango":
		return "Soy Chango, agente autónomo de Diego Dell Agostino. No soy un asistente genérico."
	}

	return normalized
}
