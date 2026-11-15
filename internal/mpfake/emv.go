package mpfake

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

func EMV(orderID, amount string) string {
	reference := uuid.NewSHA1(uuid.NameSpaceOID, []byte(orderID)).String()
	merchant := field("00", "COM.MERCADOLIBRE") + field("02", "3") + field("06", reference)
	var b strings.Builder
	b.WriteString(field("00", "01"))
	b.WriteString(field("01", "12"))
	b.WriteString(field("43", merchant))
	b.WriteString(field("52", "0000"))
	b.WriteString(field("53", "986"))
	if amount != "" {
		b.WriteString(field("54", amount))
	}
	b.WriteString(field("58", "BR"))
	b.WriteString(field("59", "Oficina Teste"))
	b.WriteString(field("60", "SAO PAULO"))
	b.WriteString(field("62", field("05", "***")))
	b.WriteString("6304")
	return b.String() + fmt.Sprintf("%04X", crc16(b.String()))
}

func field(id, value string) string {
	return fmt.Sprintf("%s%02d%s", id, len(value), value)
}

func crc16(s string) uint16 {
	crc := uint16(0xFFFF)
	for i := 0; i < len(s); i++ {
		crc ^= uint16(s[i]) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
