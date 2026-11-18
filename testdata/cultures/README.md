# Культури неба (Stellarium)

Файли `*.json` — це `index.json` відповідних каталогів
[Stellarium/skycultures](https://github.com/Stellarium/stellarium/tree/master/skycultures),
**без змін** (завантажено 2026-11-18). Для платформи взято 14 різних культур. Культури з ліцензією
CC BY-ND не брали, бо ND забороняє похідні матеріали.

Платформа читає лише `id`, `region`, `classification` і `constellations[]` (`id`, `common_name`, `lines`).
Сузір'я — це полілінії з номерів Hipparcos. Вершина, що не є зорею (координата, `DSO:M42`), розриває полілінію.

| Файл | Регіон | Сузір'їв | Ліцензія |
|---|---|---|---|
| `modern_iau.json` | світ (88 сузір'їв IAU) | 88 | CC BY-SA 4.0 |
| `egyptian.json` | Північна Африка | 28 | GNU GPL v2.0 |
| `maya.json` | Центральна Америка | 14 | CC BY-NC-SA 4.0 |
| `norse.json` | Північна Європа | 6 | CC BY-SA 4.0 |
| `inuit.json` | Північна Америка | 14 | GNU GPL v2.0 |
| `navajo.json` | Північна Америка | 8 | GNU GPL v2.0 |
| `maori.json` | Австралазія | 6 | CC BY-SA 4.0 |
| `sami.json` | Північна Європа | 10 | CC BY-SA 4.0 |
| `hawaiian_starlines.json` | Полінезія | 13 | CC BY-SA 4.0 |
| `boorong.json` | Австралазія | 29 | CC BY-SA 4.0 |
| `aztec.json` | Центральна Америка | 5 | GNU GPL v2.0 |
| `mongolian.json` | Східна Азія | 4 | CC BY-SA 4.0 |
| `tibetan.json` | Східна Азія | 18 | CC BY-SA |
| `indian.json` | Південна Азія | 49 | CC BY-SA 4.0 |

Автори і точні умови кожної культури — у `description.md` її каталогу в репозиторії Stellarium.
`maya` має ліцензію з обмеженням NC: тільки некомерційне використання (дипломна робота).

Поза цим каталогом (не з Stellarium): `../names_uk.csv` — чернетка українських назв яскравих зір.
