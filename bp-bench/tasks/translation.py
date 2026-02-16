"""Translation task type — tests agent ability to translate text between languages.

Bundles ~30 curated sentence pairs across EN↔TR, EN↔DE, EN↔ZH.
Scoring uses difflib.SequenceMatcher (stdlib-only, no BLEU dependency).
"""

from __future__ import annotations

import difflib
from pathlib import Path

from .base import TaskType, TaskResult

# ---------------------------------------------------------------------------
# Bundled translation pairs: (source_lang, target_lang, source_text, reference)
# ---------------------------------------------------------------------------

TRANSLATION_PAIRS = [
    # EN → TR (10 pairs)
    ("en", "tr",
     "The weather is beautiful today.",
     "Bugün hava çok güzel."),
    ("en", "tr",
     "Can you help me find the nearest hospital?",
     "En yakın hastaneyi bulmama yardım edebilir misiniz?"),
    ("en", "tr",
     "I would like to order a cup of coffee, please.",
     "Bir fincan kahve sipariş etmek istiyorum, lütfen."),
    ("en", "tr",
     "The train departs at half past three in the afternoon.",
     "Tren öğleden sonra üç buçukta kalkıyor."),
    ("en", "tr",
     "She has been studying computer science for four years.",
     "Dört yıldır bilgisayar bilimi okuyor."),
    ("en", "tr",
     "Please turn off your mobile phones during the meeting.",
     "Toplantı sırasında lütfen cep telefonlarınızı kapatın."),
    ("en", "tr",
     "The library closes at nine o'clock on weekdays.",
     "Kütüphane hafta içi saat dokuzda kapanıyor."),
    ("en", "tr",
     "We need to finish this project before the deadline.",
     "Bu projeyi son teslim tarihinden önce bitirmemiz gerekiyor."),
    ("en", "tr",
     "The children are playing in the park after school.",
     "Çocuklar okuldan sonra parkta oynuyorlar."),
    ("en", "tr",
     "Could you please send me the report by email?",
     "Raporu bana e-posta ile gönderebilir misiniz lütfen?"),

    # EN → DE (10 pairs)
    ("en", "de",
     "Good morning, how are you today?",
     "Guten Morgen, wie geht es Ihnen heute?"),
    ("en", "de",
     "The restaurant on the corner serves excellent Italian food.",
     "Das Restaurant an der Ecke serviert ausgezeichnetes italienisches Essen."),
    ("en", "de",
     "I need to buy a ticket for the train to Berlin.",
     "Ich muss eine Fahrkarte für den Zug nach Berlin kaufen."),
    ("en", "de",
     "The museum is open from Tuesday to Sunday.",
     "Das Museum ist von Dienstag bis Sonntag geöffnet."),
    ("en", "de",
     "She works as a software engineer at a large company.",
     "Sie arbeitet als Softwareingenieurin bei einem großen Unternehmen."),
    ("en", "de",
     "Please close the window, it is very cold outside.",
     "Bitte schließen Sie das Fenster, es ist sehr kalt draußen."),
    ("en", "de",
     "The flight from Frankfurt to New York takes about eight hours.",
     "Der Flug von Frankfurt nach New York dauert etwa acht Stunden."),
    ("en", "de",
     "We are planning a vacation to the south of France.",
     "Wir planen einen Urlaub in den Süden Frankreichs."),
    ("en", "de",
     "The students have an exam next Monday morning.",
     "Die Studenten haben nächsten Montagmorgen eine Prüfung."),
    ("en", "de",
     "Can you recommend a good book to read?",
     "Können Sie ein gutes Buch zum Lesen empfehlen?"),

    # EN → ZH (10 pairs)
    ("en", "zh",
     "Thank you very much for your help.",
     "非常感谢你的帮助。"),
    ("en", "zh",
     "What time does the meeting start tomorrow?",
     "明天的会议几点开始？"),
    ("en", "zh",
     "I have been learning Chinese for two years.",
     "我学中文已经两年了。"),
    ("en", "zh",
     "The price of this product has increased significantly.",
     "这个产品的价格大幅上涨了。"),
    ("en", "zh",
     "Please remember to bring your passport when you travel.",
     "旅行时请记得带上你的护照。"),
    ("en", "zh",
     "The new subway line will open next month.",
     "新的地铁线路下个月开通。"),
    ("en", "zh",
     "He decided to quit his job and start his own business.",
     "他决定辞职并开始自己创业。"),
    ("en", "zh",
     "The hospital is located on the east side of the city.",
     "医院位于城市的东边。"),
    ("en", "zh",
     "We should protect the environment for future generations.",
     "我们应该为子孙后代保护环境。"),
    ("en", "zh",
     "The concert was cancelled due to heavy rain.",
     "音乐会因大雨而取消了。"),
]

LANG_NAMES = {
    "en": "English",
    "tr": "Turkish",
    "de": "German",
    "zh": "Chinese",
}


class TranslationTask(TaskType):
    """Translation benchmark — agent translates text, scored by string similarity."""

    name = "translation"

    def load_tasks(self, limit: int | None = None) -> list[dict]:
        tasks = []
        for i, (src_lang, tgt_lang, source, reference) in enumerate(TRANSLATION_PAIRS):
            tasks.append({
                "task_id": f"translation/{src_lang}-{tgt_lang}/{i:03d}",
                "source_lang": src_lang,
                "target_lang": tgt_lang,
                "source_text": source,
                "reference_translation": reference,
            })
            if limit and len(tasks) >= limit:
                break
        return tasks

    def setup_workspace(self, task: dict, ws: Path) -> None:
        (ws / "source.txt").write_text(task["source_text"], encoding="utf-8")

    def setup_blueprint(self, task: dict, ws: Path) -> None:
        src_name = LANG_NAMES[task["source_lang"]]
        tgt_name = LANG_NAMES[task["target_lang"]]
        blueprint = f"""\
_meta:
  version: "1"
intent: Translate a single sentence from {src_name} to {tgt_name}
workflow:
  - step: read
    action: Read source.txt to get the input text
  - step: analyze
    action: Identify key terms, idioms, and grammatical structures
  - step: translate
    action: Produce a natural, fluent translation in {tgt_name}
    constraints:
      - Preserve the original meaning and tone
      - Use natural {tgt_name} word order and grammar
      - Translate idioms to their {tgt_name} equivalents rather than literally
      - Keep proper nouns unchanged
  - step: write
    action: Write ONLY the translated sentence to output.txt
quality:
  - Accuracy — meaning must match the source
  - Fluency — must read naturally to a native {tgt_name} speaker
  - No extra text, commentary, or explanations in the output
"""
        (ws / "BLUEPRINT.yaml").write_text(blueprint, encoding="utf-8")

    def get_instruction(self, task: dict, ws: Path) -> str:
        src_name = LANG_NAMES[task["source_lang"]]
        tgt_name = LANG_NAMES[task["target_lang"]]
        return f"""\
Translate the following text from {src_name} to {tgt_name}.

Read the file `source.txt` which contains the text to translate.
Write ONLY the translated text to `output.txt` — no explanations, no extra text.
Then call give_result("done").
"""

    def get_blueprint_instruction(self, task: dict, ws: Path) -> str:
        src_name = LANG_NAMES[task["source_lang"]]
        tgt_name = LANG_NAMES[task["target_lang"]]
        return f"""\
You are in a workspace with a translation task.
There is a BLUEPRINT.yaml describing the workflow — read it first.

Then follow the workflow steps to translate the text in `source.txt`
from {src_name} to {tgt_name}.

Write ONLY the translated text to `output.txt`.
Then call give_result("done").
"""

    def score(self, task: dict, ws: Path) -> TaskResult:
        output_file = ws / "output.txt"
        if not output_file.exists():
            return TaskResult(
                task_id=task["task_id"],
                passed=0,
                total=1,
                score=0.0,
                details={"error": "output.txt not found"},
            )

        predicted = output_file.read_text(encoding="utf-8-sig").strip()
        reference = task["reference_translation"].strip()

        if not predicted:
            return TaskResult(
                task_id=task["task_id"],
                passed=0,
                total=1,
                score=0.0,
                details={"error": "output.txt is empty"},
            )

        # Normalize for comparison: lowercase, strip punctuation edges
        pred_norm = predicted.lower().strip()
        ref_norm = reference.lower().strip()

        similarity = difflib.SequenceMatcher(None, pred_norm, ref_norm).ratio()
        # Count as "passed" if similarity >= 0.6
        passed = 1 if similarity >= 0.6 else 0

        return TaskResult(
            task_id=task["task_id"],
            passed=passed,
            total=1,
            score=round(similarity, 3),
            details={
                "predicted": predicted,
                "reference": reference,
                "similarity": round(similarity, 3),
            },
        )
