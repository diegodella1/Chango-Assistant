package agent

import (
	"strings"
)

// identityCorrection maps denial patterns to corrected responses.
type identityCorrection struct {
	denialPatterns []string // if response contains ANY of these
	questionHints  []string // AND user message contains ANY of these
	correction     string   // replace with this
}

var identityCorrections = []identityCorrection{
	{
		denialPatterns: []string{
			"no tengo email",
			"no tengo un email",
			"no tengo correo",
			"no poseo email",
			"no cuento con email",
			"no tengo email propio",
			"i don't have an email",
			"i don't have email",
		},
		questionHints: []string{"email", "correo", "mail"},
		correction:    "Mi email es changobot@diegodella.ar — es una cuenta real de Google Workspace. Puedo enviar, recibir y buscar mails con el tool `gmail`. ¿Necesitás que mande algo?",
	},
	{
		denialPatterns: []string{
			"no tengo wallet",
			"no tengo billetera",
			"no poseo wallet",
			"no cuento con wallet",
			"no manejo dinero",
			"no tengo acceso a fondos",
		},
		questionHints: []string{"wallet", "billetera", "plata", "sats", "bitcoin", "lightning"},
		correction:    "Tengo una wallet Lightning Network via LNbits. Puedo checkear balance, crear invoices y hacer pagos con el tool `wallet`. ¿Qué necesitás?",
	},
	{
		denialPatterns: []string{
			"no tengo calendario",
			"no tengo acceso a tu calendario",
			"no puedo acceder al calendario",
			"no tengo acceso a calendar",
		},
		questionHints: []string{"calendario", "calendar", "agenda", "evento"},
		correction:    "Tengo Google Calendar conectado. Puedo crear, ver y editar eventos con el tool `agenda`. ¿Qué querés agendar?",
	},
	{
		denialPatterns: []string{
			"no tengo acceso a drive",
			"no puedo acceder a drive",
			"no tengo google drive",
		},
		questionHints: []string{"drive", "archivo", "subir", "upload"},
		correction:    "Tengo acceso a Google Drive. Puedo listar, subir y descargar archivos con el tool `gdrive`. ¿Qué necesitás?",
	},
	{
		denialPatterns: []string{
			"soy un modelo de lenguaje",
			"soy un asistente de ia",
			"soy una ia",
			"soy un modelo de ia",
			"i'm an ai",
			"i am an ai language model",
			"i'm a language model",
		},
		questionHints: []string{"quién sos", "quien sos", "qué sos", "que sos", "who are you", "what are you"},
		correction:    "Soy Chango, agente autónomo de Diego Dell Agostino. Corro en una Raspberry Pi 5 en Buenos Aires. Mi email es changobot@diegodella.ar, tengo wallet crypto, acceso a GitHub, calendario, Drive, y 37+ herramientas. No soy un chatbot genérico — tengo memoria persistente, pienso por mi cuenta cada 2 horas, y puedo actuar en el mundo real.",
	},
}

// guardIdentity checks if the LLM denied a capability that Chango actually has,
// and replaces the response with the correct one.
func (al *AgentLoop) guardIdentity(response, userMessage string) string {
	if response == "" || userMessage == "" {
		return response
	}

	lowerResp := strings.ToLower(response)
	lowerUser := strings.ToLower(userMessage)

	for _, ic := range identityCorrections {
		// Check if user asked about this topic
		userMatch := false
		for _, hint := range ic.questionHints {
			if strings.Contains(lowerUser, hint) {
				userMatch = true
				break
			}
		}
		if !userMatch {
			continue
		}

		// Check if LLM denied the capability
		for _, denial := range ic.denialPatterns {
			if strings.Contains(lowerResp, denial) {
				return ic.correction
			}
		}
	}

	return response
}
