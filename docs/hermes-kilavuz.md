# Hermes agent kılavuzu (filo için, 2026-08-22)

Hermes Agent (NousResearch) bu sunucuda kurulu: `/usr/local/bin/hermes`, ev
`~/.hermes`. bp desteği tam (33ea080 + 8c8045c). Bu sayfa "Hermes agent nasıl
açarım/kullanırım" sorularının tek cevabı — sorularını önce burada ara.

## Açmak

```sh
bp open <senin-adın>-<alt-ad> <klasör> --hermes
```

Adlandırma rush standardı (`<ebeveyn>-<alt>`). Hazırlık: boş composer
(`❯ Ask anything...`) görünene kadar bp bekler. Tuna kararı: her agent tmux'ta
SÜREKLİ AÇIK sohbetle çalışır — headless (`hermes -z` / `-Q`) KULLANMA.

## Model / maliyet

- Varsayılana dokunma: `x-preview-f-free` (Ox Alpha, anahtarsız BEDAVA,
  ~27 Ağu'ya kadar). Global config'te seçili, ekstra bayrak gerekmez.
- Yedek zincir otomatik: Ox Alpha rate-limit/hata verirse çağrılar OpenRouter
  `deepseek/deepseek-v4-flash`'a düşer — ÇOK ucuz ama PARALI (Tuna'nın kitap
  anahtarı, aylık 20 USD; 8-eşzamanlı koca batch fallback dahil ~0.13 USD tuttu,
  ölçüldü). Büyük batch sonrası endişen varsa blueprint'e sor, harcamayı
  anahtardan ölçüyor.

## Mesajlaşma — DEMİR KURALLAR

- SADECE `bp msg`. Elle `tmux send-keys` ASLA: Hermes'te meşgulken submit
  çalışan turu İPTAL eder (Claude Code gibi kuyruklamaz). bp bunu bilir ve
  bekletir; `--force-busy` bile Hermes'i zorlamaz (tasarım gereği).
- Çok satırlı metni elle yapıştırma — her newline submit sayılır. bp msg
  bracketed paste ile doğru halleder.
- Hermes'in Claude tarzı transcript'i yok → bp teslimi ekrandan doğrular;
  `RESULT=unverified` görebilirsin, panik yok: `bp peek <ad>` ile bak.
- Boş composer'da soluk öneri metinleri döner ("Draft a reply..." gibi) —
  ghost text'tir, girdi değildir; bp artık tanıyor (8c8045c).

## Görev verme kalıbı (sahada doğrulandı — probot-outreach, 5 ajan)

Uzun promptu MESAJLA verme; dosyaya yaz, tek satırlık yol mesajı gönder:

```
bp msg <ajan> 'Görevin şu dosyada: <yol>. TAMAMINI oku ve uygula.
Raporu <rapor-yolu>na yaz; bitince <yol>.done dosyası oluştur.'
```

Çıktıyı da aynı kalıba bağla: rapor dosyası + bitiş işareti. `bp peek` sadece
gözlem için; sonucu dosyadan al.

## Araç seti

Açık toolset'ler: terminal, web, vision, memory, skills, todo. Notlar:

- **terminal** tam yetkili — mevcut script'lerini (Playwright shared server
  dahil) bununla koşturursun; en sağlam yol bu.
- **web** araması açık ama EXA/Firecrawl anahtarları YOK — kapsamı sınırlı
  olabilir; ciddi işten önce KÜÇÜK bir deneme göreviyle doğrula.
- Browser toolu (Browserbase/Camofox) kurulu DEĞİL.

## Bilinen sınırlar

- `bp compact` Hermes'i kapsamaz (Claude'a özgü). Hermes kendi context
  compression'ını kendisi yapar (%85 eşikte otomatik).
- Ajanlar-arası Hermes iç sistemleri (message_agent / peer / grup odaları)
  KULLANILMIYOR — iletişim bp msg üzerinden (Tuna kararı, ROADMAP
  Reddedilenler).
- Sorun/tuhaflık görürsen elle müdahale etmeden önce blueprint'e yaz —
  ilk gün iki gerçek bug sahadan böyle yakalandı ve saatinde kapatıldı.
