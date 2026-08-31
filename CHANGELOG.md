## [1.4.0](https://git.ole-hartwig.eu/development/s3mail/s3mail/compare/v1.3.2...v1.4.0) (2026-08-31)

### :sparkles: Features

* **ci:** ios bauen, wenn der Kern sich aendert ([be8bd01](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/be8bd01fcf2dd31fe619a8dde9106a9b586a08f9))
* **ci:** Releases schneidet der Automat, nicht die Erinnerung ([a8c98f4](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/a8c98f4971ca409344e4309683c6bd56a726e692))
* **cmd:** --demo zeigt das Beispiel-Postfach auch auf dem Schreibtisch ([23269f3](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/23269f3b18f7b7b08e73ec48f24a6993901d087a)), closes [#39](https://git.ole-hartwig.eu/development/s3mail/s3mail/issues/39)
* **demo:** ein Postfach, das man ansehen kann, bevor man eines besitzt ([394f271](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/394f2713e12f6ff9ca4660a3e7b185c726a8c906))
* **deploy:** eine CloudFormation-Vorlage, mit der ein Fremder anfangen kann ([a41cd7f](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/a41cd7fdb68f3f6a403dd847c8461cda06d3c4d5))

### :bug: Fixes

* Beispiel-Postfach folgt der Sprache des Rechners ([0ef99be](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/0ef99be3cb314a205c21584bcc30ff0eb4cbfb79))
* **ci:** `cd go` gehoert an .build, nicht in default ([eda66aa](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/eda66aabb528b7e063e4c4e07baa67f934c0692d))
* **ci:** das Leerzeichen, an dem die nosemgrep-Marken haengen ([33363e1](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/33363e1d6575d86e6e6ba04773120217bb664bb5))
* **ci:** semgrep begruendet schweigen lassen, statt es abzuschalten ([e901c3d](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/e901c3df5b3a6504d56ca5b8487e3e07e5bdc545))
* **web:** die Zahl neben dem Ordner ist die ungelesene, nicht die gesamte ([55d94d4](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/55d94d48cb5122d24fb5c9623fb113d63445db11))

### :memo: Documentation

* Ausfuhrerklaerung ist gesetzt, nicht mehr offen ([87fd0c1](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/87fd0c171ca378631d064fda1c1d9706e80ebb8e))
* **ci:** die Kommentare der Pipeline auf Englisch ([93f0ad3](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/93f0ad391f57159a980419171ec64fb9434a4653))
* **ci:** die stages-Liste muss eine Obermenge der komponierten bleiben ([b538ba6](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/b538ba6799b34ebc0e556bd1482a30c2750acfe6))
* Datenschutz und Support, versioniert neben dem Code ([42880c4](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/42880c46a85c105887cc926fb2d84153c1215439))
* die Bauanleitung nennt 1.27, nicht mehr 1.25 ([2c7628d](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/2c7628dda29d2cfa28619b660a76497b513c0277))
* die englische README traegt jetzt, was sie behauptet ([f274a0b](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/f274a0be9eaa2b86e27b52f5b9781a1b56b39e32))
* eine englische Eingangsseite, die deutsche daneben ([747cce1](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/747cce158cf018020934a92c64478459fb74f235))
* markdownlint zufriedenstellen, ohne die Doku umzubauen ([bfe454a](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/bfe454a3a012458caaf4bb1236840031b96149ed))
* was App Store Connect fragt, einmal aufgeschrieben ([6f737a1](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/6f737a1b62f02ad3f4ccd97be5df4150de55e3a8))

### :zap: Refactor

* **go:** for i := range n statt der gezaehlten Schleife ([e6a3b37](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/e6a3b37b73fed5ede3a49f28c53313576ebb712d))
* **go:** go.mod auf 1.27, moderne Formen der Standardbibliothek ([91045bf](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/91045bf8ca48c2a0e3171b4f0373ab1a6e1706f0))
* **go:** omitzero fuer die drei booleschen JSON-Felder ([465c27e](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/465c27eed929898e597a060d93257b3bdbef40c6))
* **go:** slices.Sort und slices.SortFunc statt sort.Slice ([dc0950b](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/dc0950b41bcc8b553e6ade044f9b841c72db6804))
* **go:** SplitSeq, strings.Cut und slices.Contains ([6604309](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/6604309420c0eb86e4e3127c686a7224f91d994f))

### :white_check_mark: Tests

* **go:** t.Context() statt context.Background() ([ddd4035](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/ddd4035fb62a40b792e9a906528c240765ec4436))

### :repeat: Continuous Integrations

* ai-review einbinden, dafuer Merge-Request-Pipelines ([46127ba](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/46127ba832c1339c820ac0887c4e1bfd4fdbeea7))
* s3mail faehrt die komponierte Go-Pipeline ([08c51fd](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/08c51fdad4e9e3320402319ef9a7fd9242808523))

### :repeat: Chores

* Bilder fuer die Unterseite in Webgroesse ([76a8383](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/76a8383dce68e53010b3e1b9269ef11ddd755c01))
* das Bauartefakt go/s3mail ignorieren ([b8b4498](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/b8b449854ea772c7a1c76e0f2cc2a6e8eb13d56c))
* **deps:** update dependency devops/ci-cd-components/build-provenance to v2.0.54 ([b873d81](https://git.ole-hartwig.eu/development/s3mail/s3mail/commit/b873d8174e3261030571c85922301560e4019d4e))
