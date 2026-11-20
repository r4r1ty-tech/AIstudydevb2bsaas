package llm

import (
	"crypto/sha256"
	"math/rand"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
)

// GlobalPoolChain — общий пул для консенсус-решения тестов: все ключи из .env
// (Gemini, OpenCode), все личные ключи из БД (они регистрируются через
// RegisterSharedPoolKeys) и ключ админа cheaper. В отличие от PoolChain,
// здесь нет разделения «свой/чужой»: это явно общий пул по требованию
// владельца, поэтому админский ключ тоже участвует.
//
// Для каждой модели ключи перемешиваются, а весь список — ещё раз, чтобы
// три запроса не всегда уходили в одни и те же ключи.
func GlobalPoolChain(cfg *config.Config) []Target {
	if cfg == nil {
		return nil
	}
	provs := Providers(cfg)
	for _, id := range []string{Gemini, OpenCode, OpenCodeGo} {
		shared := sharedKeysFor(id)
		idx := -1
		for i := range provs {
			if provs[i].ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			// В .env ключей нет, но в общем пуле есть личные/расшаренные —
			// провайдер всё равно должен участвовать.
			if len(shared) == 0 {
				continue
			}
			bp, ok := BuiltinProvider(id)
			if !ok {
				continue
			}
			provs = append(provs, bp)
			idx = len(provs) - 1
		}
		provs[idx].Keys = mergePoolKeys(provs[idx].Keys, shared)
	}
	var out []Target
	for _, p := range provs {
		if len(p.Keys) == 0 || len(p.Models) == 0 {
			continue
		}
		indices := make([]int, len(p.Keys))
		for i := range indices {
			indices[i] = i
		}
		if len(indices) > 1 {
			//nolint:gosec // перемешивание ключей, не криптография
			rand.Shuffle(len(indices), func(i, j int) {
				indices[i], indices[j] = indices[j], indices[i]
			})
		}
		for _, m := range p.Models {
			for _, idx := range indices {
				out = append(out, target(p, m, idx))
			}
		}
	}
	if len(out) > 1 {
		// Перемешиваем только когда ключей больше одного: у одного ключа
		// порядок моделей — это приоритет (сильная первой), его нельзя терять.
		keys := map[[32]byte]bool{}
		for _, t := range out {
			if h, ok := t.SecretHash(); ok {
				keys[h] = true
			}
		}
		if len(keys) > 1 {
			//nolint:gosec // перемешивание ключей, не криптография
			rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		}
	}
	return out
}

// DistinctKeyTargets оставляет по одному таргету на каждый уникальный API-ключ.
// Для консенсуса нужны именно разные ключи, а не пять моделей одного ключа.
// max <= 0 — без ограничения.
func DistinctKeyTargets(chain []Target, max int) []Target {
	var out []Target
	seen := make(map[[32]byte]bool, len(chain))
	usedModels := map[string]bool{}
	keyOf := func(t Target) [32]byte {
		if h, ok := t.SecretHash(); ok {
			return h
		}
		// Без секрета (тестовый таргет) ключом считаем провайдера и подпись.
		return sha256.Sum256([]byte(t.Provider + "|" + t.KeyLabel))
	}
	// Первый проход — по одному ключу на модель, чтобы три голоса не оказались
	// тремя запусками одной и той же модели. Второй — добираем оставшиеся.
	for pass := 0; pass < 2; pass++ {
		for _, t := range chain {
			h := keyOf(t)
			if seen[h] {
				continue
			}
			if pass == 0 && usedModels[t.Model] {
				continue
			}
			seen[h] = true
			usedModels[t.Model] = true
			out = append(out, t)
			if max > 0 && len(out) >= max {
				return out
			}
		}
	}
	return out
}
