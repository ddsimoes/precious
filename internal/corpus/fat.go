package corpus

import (
	"fmt"
	"sync"
	"time"
)

// FATFixture returns the small tree for FAT-capability tests (R1.17): a
// memory card's folders and files, every modification time on an even
// second, as FAT's 2-second stamps store them, and only names FAT can hold.
func FATFixture() *Tree { return fatOnce() }

var fatOnce = sync.OnceValue(buildFAT)

func buildFAT() *Tree {
	d := newDef()
	shot := at(2008, 10, 18, 15, 30).Add(14 * time.Second)
	for i := range 4 {
		p := fmt.Sprintf("DCIM/100CANON/IMG_%04d.JPG", 1201+i)
		d.file(p, shot.Add(time.Duration(i)*42*time.Second), photo(p, 120, 90))
	}
	d.file("MUSICA/faixa01.mp3", at(2008, 9, 1, 20, 0).Add(6*time.Second), mp3("FAT faixa01", 90_000))
	d.file("MUSICA/faixa02.mp3", at(2008, 9, 1, 20, 4).Add(58*time.Second), mp3("FAT faixa02", 80_000))
	d.file("Documentos/lista.txt", at(2008, 11, 2, 9, 0).Add(2*time.Second), []byte("pao\r\nleite\r\ncafe\r\n"))
	d.file("Documentos/Relatorio.doc", at(2008, 11, 3, 18, 21).Add(36*time.Second), ole("FAT Relatorio", 19_456))
	d.file("AUTORUN.INF", at(2008, 8, 8, 8, 8).Add(8*time.Second), []byte("[autorun]\r\nicon=cartao.ico\r\n"))
	d.file("System Volume Information/IndexerVolumeGuid", at(2008, 8, 8, 8, 10), random("FAT guid", 76))
	return d.finish(nil)
}
