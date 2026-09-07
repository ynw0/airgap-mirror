package pack
import("fmt";"path";"strings")
func ValidateLogicalPath(p string)error{if p==""||strings.ContainsRune(p,'\x00'){return fmt.Errorf("invalid logical path")};if strings.Contains(p,"\\")||strings.HasPrefix(p,"/")||len(p)>=2&&p[1]==':'{return fmt.Errorf("unsafe logical path %q",p)};c:=path.Clean(p);if c=="."||c!=p||c==".."||strings.HasPrefix(c,"../"){return fmt.Errorf("unsafe logical path %q",p)};return nil}
