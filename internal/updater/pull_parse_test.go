package updater

import "testing"

func TestPullIndicatesNoNewImage(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want bool
	}{
		{
			name: "compose_v2_skipped",
			out:  " ✔ app Skipped - Image is up to date    0.0s \n",
			want: true,
		},
		{
			name: "image_up_to_date_text",
			out:  "Image is up to date\n",
			want: true,
		},
		{
			name: "has_download_layer",
			out:  "Pulling fs layer abc\n",
			want: false,
		},
		{
			name: "downloaded_newer",
			out:  "Downloaded newer image for x\n",
			want: false,
		},
		{
			name: "build_only_no_pullable_image",
			out:  "service app was skipped because it has no image to be pulled\n",
			want: true,
		},
		{
			name: "build_only_from_source",
			out:  "WARNING: Some service image(s) must be built from source by running: docker compose build app\n",
			want: true,
		},
		{
			name: "empty",
			out:  "",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PullIndicatesNoNewImage(tt.out); got != tt.want {
				t.Fatalf("PullIndicatesNoNewImage() = %v, want %v", got, tt.want)
			}
		})
	}
}
