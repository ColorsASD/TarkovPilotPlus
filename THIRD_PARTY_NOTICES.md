# Third-party components

Item recognition uses the following local OCR components. The application does not send screenshots to an external OCR service; item data comes from the Tarkov Market API.

| Component | Version / source | License |
| --- | --- | --- |
| Tesseract .NET | [Charles Weld, 5.2.0](https://github.com/charlesw/tesseract/tree/2c993543f7fa66576a8890a6c4ab053c4598aaed), NuGet `Tesseract` 5.2.0 | Apache License 2.0 |
| Tesseract native engine | `tesseract50.dll` from the NuGet package above; the engine reports version 5.0.0 | Apache License 2.0 |
| Leptonica | `leptonica-1.82.0.dll` from the NuGet package above | BSD 2-Clause |
| InteropDotNet | Component embedded in the Tesseract .NET wrapper; Andrey Akinshin | MIT |
| English and Russian OCR language models | [tessdata_fast](https://github.com/tesseract-ocr/tessdata_fast/tree/87416418657359cb625c412a48b6e1d6d41c29bd), commit `87416418657359cb625c412a48b6e1d6d41c29bd` | Apache License 2.0 |

- Tesseract .NET: Copyright 2012-2022 Charles Weld. NuGet package metadata: Copyright 2012-2020 Charles Weld.
- Tesseract: the project's authors and contributors; the full license text is included in `ocr/licenses/Tesseract-LICENSE.txt`.
- Leptonica: Copyright (C) 2001-2020 Leptonica. All rights reserved.
- InteropDotNet: Copyright (c) 2014 Andrey Akinshin.

[app/plus/ocr/prepare.ps1](app/plus/ocr/prepare.ps1) prepares the build using components downloaded from official sources at pinned versions. The NuGet package, extracted DLLs, language models and license files are verified with SHA256 hashes. The application does not download OCR components at runtime.

The full licenses and this document are included in the `ocr` folder next to the application. Keep the `ocr` folder and its licenses when distributing the application. Microsoft .NET Framework 4.8, Windows PowerShell 5.1 and the Microsoft Visual C++ 2015–2022 x64 runtime are system dependencies and are not included in the OCR package.
