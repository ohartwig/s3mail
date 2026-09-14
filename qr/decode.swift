// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0
//
// Reads QR codes with Apple's Vision framework. Used by qr_vision_test.go to
// check this package's encoder against a decoder nobody here wrote.
//
// Takes PNG paths, prints one line per file: the payload, or "<none>".

import CoreImage
import Foundation
import Vision

for path in CommandLine.arguments.dropFirst() {
    guard let img = CIImage(contentsOf: URL(fileURLWithPath: path)),
          let cg = CIContext().createCGImage(img, from: img.extent) else {
        print("<unreadable>")
        continue
    }
    let req = VNDetectBarcodesRequest()
    req.symbologies = [.qr]
    do {
        try VNImageRequestHandler(cgImage: cg).perform([req])
        let found = (req.results ?? []).compactMap { $0.payloadStringValue }
        print(found.first ?? "<none>")
    } catch {
        print("<error: \(error.localizedDescription)>")
    }
}
