package agent

import (
	"strings"
)

// capabilityGuard defines a capability Chango has, with negation detection.
type capabilityGuard struct {
	key           string
	topicWords    []string // words that identify the topic (email, wallet, etc.)
	negationWords []string // words that signal denial (no tengo, don't have, etc.)
	questionHints []string // user message must contain one of these
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
		key:           "email",
		topicWords:    []string{"email", "correo", "mail", "mailbox", "casilla", "gmail", "cuenta de email"},
		questionHints: []string{"email", "correo", "mail", "cuenta"},
	},
	{
		key:           "wallet",
		topicWords:    []string{"wallet", "billetera", "fondos", "dinero", "sats", "bitcoin", "lightning"},
		questionHints: []string{"wallet", "billetera", "plata", "sats", "bitcoin", "lightning"},
	},
	{
		key:           "calendar",
		topicWords:    []string{"calendario", "calendar", "agenda", "eventos"},
		questionHints: []string{"calendario", "calendar", "agenda", "evento"},
	},
	{
		key:           "drive",
		topicWords:    []string{"drive", "archivos", "google drive"},
		questionHints: []string{"drive", "archivo", "subir", "upload"},
	},
	{
		key:           "identity",
		topicWords:    []string{"modelo de lenguaje", "asistente de ia", "una ia", "modelo de ia", "an ai", "language model", "a language model"},
		questionHints: []string{"quién sos", "quien sos", "qué sos", "que sos", "who are you", "what are you"},
	},
}

func (al *AgentLoop) hasCapability(key string) bool {
	if al == nil || al.tools == nil {
		return false
	}

	switch key {
	case "email":
		_, ok := al.tools.Get("gmail")
		return ok
	case "wallet":
		_, ok := al.tools.Get("wallet")
		return ok
	case "calendar":
		if _, ok := al.tools.Get("calendar"); ok {
			return true
		}
		_, ok := al.tools.Get("agenda")
		return ok
	case "drive":
		_, ok := al.tools.Get("gdrive")
		return ok
	case "identity":
		return true
	default:
		return false
	}
}

func (al *AgentLoop) capabilityCorrection(key string) string {
	switch key {
	case "email":
		return "Mi email es changobot@diegodella.ar y en este runtime tengo el tool `gmail` activo para buscar, leer, enviar y responder mails."
	case "wallet":
		return "Tengo wallet Lightning Network via `wallet`. Puedo revisar balance, crear invoices y pagar si la wallet está configurada."
	case "calendar":
		if _, ok := al.tools.Get("calendar"); ok {
			return "Tengo Google Calendar activo con el tool `calendar`. Puedo listar, crear, editar y borrar eventos."
		}
		return "Tengo agenda activa con el tool `agenda`. Puedo crear, listar, editar y borrar eventos en mi agenda local."
	case "drive":
		return "Tengo Google Drive activo con el tool `gdrive`. Puedo listar, subir y descargar archivos."
	case "identity":
		parts := []string{
			"Soy Chango, agente autónomo de Diego Dell Agostino.",
			"Corro en una Raspberry Pi 5 en Buenos Aires.",
		}
		if al.hasCapability("email") {
			parts = append(parts, "Tengo email propio: changobot@diegodella.ar.")
		}
		if al.hasCapability("wallet") {
			parts = append(parts, "Tengo wallet Lightning.")
		}
		if al.hasCapability("calendar") {
			parts = append(parts, "Tengo calendario/agenda activa.")
		}
		if al.hasCapability("drive") {
			parts = append(parts, "Tengo Google Drive activo.")
		}
		parts = append(parts, "No soy un chatbot genérico: tengo memoria persistente y herramientas reales.")
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

// guardIdentity checks if the LLM denied a capability that Chango actually has.
// Uses co-occurrence detection: if the response contains BOTH a negation marker AND
// a topic word near each other, it's a denial — regardless of exact phrasing.
func (al *AgentLoop) guardIdentity(response, userMessage string) string {
	if response == "" || userMessage == "" {
		return response
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
				if al.hasCapability(cg.key) {
					if correction := al.capabilityCorrection(cg.key); correction != "" {
						return correction
					}
				}
				break
			}
		}
	}

	return response
}
