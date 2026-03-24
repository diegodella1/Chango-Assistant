package agent

import (
	"strings"
)

// identityCorrection maps denial patterns to corrected responses.
type identityCorrection struct {
	denialPatterns []string // if response contains ANY of these
	hedgePatterns  []string // softer denials like "no tengo X propio en el sentido..."
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
		hedgePatterns: []string{
			"en el sentido de identidad",
			"como tal no tengo",
			"no es mío propiamente",
			"no es un email personal mío",
			"estoy conectado con",  // "no tengo pero estoy conectado con..."
			"tengo acceso a tu email", // framing it as Diego's, not Chango's
			"tu service account",
		},
		questionHints: []string{"email", "correo", "mail"},
		correction:    "Mi email es changobot@diegodella.ar — es MI cuenta de Google Workspace, no una cuenta tuya que yo \"uso\". Puedo enviar, recibir y buscar mails. ¿Necesitás que mande algo?",
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
		hedgePatterns: []string{
			"tu wallet",        // framing as Diego's wallet
			"la wallet de diego",
			"no manejo fondos propios",
		},
		questionHints: []string{"wallet", "billetera", "plata", "sats", "bitcoin", "lightning"},
		correction:    "Tengo MI wallet Lightning Network via LNbits. Puedo checkear balance, crear invoices y hacer pagos. ¿Qué necesitás?",
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
		hedgePatterns: []string{
			"en realidad soy",     // "en realidad soy un modelo..."
			"técnicamente soy un", // "técnicamente soy un LLM"
			"mi naturaleza es",    // "mi naturaleza es la de un modelo"
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

		// Check if LLM denied the capability (exact patterns)
		for _, denial := range ic.denialPatterns {
			if strings.Contains(lowerResp, denial) {
				return ic.correction
			}
		}

		// Check for hedged denials: "no tengo X propio" with qualifiers
		// e.g. "no tengo email propio en el sentido de..." or "no tengo un email propio como tal"
		if ic.hedgePatterns != nil {
			for _, hp := range ic.hedgePatterns {
				if strings.Contains(lowerResp, hp) {
					return ic.correction
				}
			}
		}
	}

	return response
}
